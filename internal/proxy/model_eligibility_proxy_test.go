package proxy_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/proxy"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/store/sqlite"
)

// upstreamResponder decides the upstream reply for an Authorization token and
// request body, letting each test model its own quota behaviour.
type upstreamResponder func(token, body string) (status int, payload string)

// eligibilityFixture wires a SQLite-backed account pool to a proxy handler and a
// stub upstream that echoes the serving account's token in its response body.
type eligibilityFixture struct {
	ctx     context.Context
	db      *sqlite.DB
	repo    *sqlite.AccountRepository
	handler *proxy.ProxyHandler
	hits    *int64
}

func newEligibilityFixture(t *testing.T, accounts []*domain.Account, respond upstreamResponder) *eligibilityFixture {
	t.Helper()

	db, err := sqlite.Open(filepath.Join(t.TempDir(), "proxy_eligibility.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := sqlite.NewAccountRepository(db)

	for _, acc := range accounts {
		if acc.Status == "" {
			acc.Status = domain.AccountStatusActive
		}
		if err := repo.Create(ctx, acc); err != nil {
			t.Fatalf("Create %s: %v", acc.ID, err)
		}
	}

	var hits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var body string
		if r.Body != nil {
			raw, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
			body = string(raw)
		}
		status, payload := http.StatusOK, `{"ok":true,"account":"`+token+`"}`
		if respond != nil {
			status, payload = respond(token, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(upstream.Close)

	handler, err := proxy.NewProxyHandler(repo, proxy.WithTargetURL(upstream.URL))
	if err != nil {
		t.Fatalf("NewProxyHandler: %v", err)
	}

	return &eligibilityFixture{ctx: ctx, db: db, repo: repo, handler: handler, hits: &hits}
}

func (f *eligibilityFixture) seedModels(t *testing.T, accountID string, models ...*domain.ModelInfo) {
	t.Helper()
	if err := f.repo.UpsertAccountModels(f.ctx, accountID, models); err != nil {
		t.Fatalf("UpsertAccountModels %s: %v", accountID, err)
	}
}

// requestModel performs a Cloud Code generateContent call and returns the recorder.
func (f *eligibilityFixture) requestModel(t *testing.T, model string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    model,
		"contents": []map[string]any{{"parts": []map[string]string{{"text": "hi"}}}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1internal:generateContent", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f *eligibilityFixture) activeAccount(t *testing.T, category domain.ModelCategory) string {
	t.Helper()
	acc, err := f.repo.GetActiveForCategory(f.ctx, category)
	if err != nil || acc == nil {
		return ""
	}
	return acc.ID
}

// newTwoAccountPool mirrors the reported setup: a Paid account that can serve
// Claude Opus 5.5, Gemini 4 Argon and Gemini, plus a Trial account limited to
// Claude Opus 4.6 and Gemini.
func newTwoAccountPool(t *testing.T, respond upstreamResponder) *eligibilityFixture {
	t.Helper()
	f := newEligibilityFixture(t, []*domain.Account{
		{ID: "acc-paid", Email: "paid@example.com", AccessToken: "paid-token", IsActive: true},
		{ID: "acc-trial", Email: "trial@example.com", AccessToken: "trial-token"},
	}, respond)

	f.seedModels(t, "acc-paid",
		&domain.ModelInfo{ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5", Category: "claude_gpt"},
		&domain.ModelInfo{ID: "gemini-4-argon-medium", DisplayName: "Gemini 4 Argon (Medium)", Category: "gemini"},
		&domain.ModelInfo{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Category: "gemini"},
	)
	f.seedModels(t, "acc-trial",
		&domain.ModelInfo{ID: "claude-opus-4-6-thinking", DisplayName: "Claude Opus 4.6 (Thinking)", Category: "claude_gpt"},
		&domain.ModelInfo{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Category: "gemini"},
	)
	return f
}

func TestPreFlightEligibility_RoutesIneligibleModelToEligibleAccount(t *testing.T) {
	f := newTwoAccountPool(t, nil)

	// Claude/GPT traffic is pinned to the Trial account, which cannot serve Opus 5.5.
	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryClaudeGPT, "acc-trial"); err != nil {
		t.Fatalf("pin claude_gpt to trial: %v", err)
	}

	rec := f.requestModel(t, "claude-opus-5-5")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 routed to paid account, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "paid-token") {
		t.Errorf("expected request dispatched via paid account, got: %s", rec.Body.String())
	}
	if got := atomic.LoadInt64(f.hits); got != 1 {
		t.Errorf("expected exactly 1 upstream hit, got %d", got)
	}
	// Eligibility is a per-model constraint: the category pin must stay on the
	// preferred account so other Claude models keep using it.
	if got := f.activeAccount(t, domain.CategoryClaudeGPT); got != "acc-trial" {
		t.Errorf("claude_gpt pin must stay on acc-trial, got %q", got)
	}
}

func TestPreFlightEligibility_EligibleModelStaysOnActiveAccount(t *testing.T) {
	f := newTwoAccountPool(t, nil)

	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryClaudeGPT, "acc-trial"); err != nil {
		t.Fatalf("pin claude_gpt to trial: %v", err)
	}

	rec := f.requestModel(t, "claude-opus-4-6-thinking")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on trial, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "trial-token") {
		t.Errorf("expected request to stay on the pinned trial account, got: %s", rec.Body.String())
	}
	if got := atomic.LoadInt64(f.hits); got != 1 {
		t.Errorf("expected exactly 1 upstream hit, got %d", got)
	}
}

func TestGeminiArgonSharesGeminiQuota_ExhaustionBlocksBoth(t *testing.T) {
	f := newTwoAccountPool(t, nil)

	f.seedModels(t, "acc-paid",
		&domain.ModelInfo{ID: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", Category: "gemini"},
	)
	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGemini, "acc-paid"); err != nil {
		t.Fatalf("pin gemini to paid: %v", err)
	}

	// Exhausting shared gemini-5h on acc-paid rotates Gemini (and Argon) to acc-trial for regular Gemini,
	// while Argon still requires model entitlement.
	reset := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	if _, err := f.db.ExecContext(f.ctx, `
		INSERT INTO quota_buckets (account_id, bucket_id, display_name, window, remaining_fraction, reset_time, updated_at)
		VALUES ('acc-paid', 'gemini-5h', 'Gemini (5h)', '5h', 0.0, ?, ?)
	`, reset, reset); err != nil {
		t.Fatalf("seed gemini bucket: %v", err)
	}

	// Regular Gemini request rotates to acc-trial.
	rec := f.requestModel(t, "gemini-2.5-pro")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for regular gemini via acc-trial, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "trial-token") {
		t.Errorf("expected regular gemini to rotate to acc-trial, got: %s", rec.Body.String())
	}

	// Argon request returns 503 because acc-paid's gemini-5h is exhausted and acc-trial lacks Argon entitlement.
	rec = f.requestModel(t, "gemini-4-argon-medium")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for argon when paid gemini-5h exhausted and trial unentitled, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestExhaustionOnArgon_RotatesSharedGeminiCategory(t *testing.T) {
	f := newTwoAccountPool(t, nil)

	f.seedModels(t, "acc-trial",
		&domain.ModelInfo{ID: "gemini-4-argon-medium", DisplayName: "Gemini 4 Argon (Medium)", Category: "gemini"},
	)

	f.seedModels(t, "acc-paid",
		&domain.ModelInfo{ID: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", Category: "gemini"},
	)
	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGemini, "acc-paid"); err != nil {
		t.Fatalf("pin gemini to paid: %v", err)
	}

	paid, err := f.repo.GetByID(f.ctx, "acc-paid")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	engine := proxy.NewFailoverEngine(f.repo, nil, nil)
	if _, err := engine.RotateAccountForCategory(f.ctx, paid, domain.CategoryGemini); err != nil {
		t.Fatalf("RotateAccountForCategory(gemini): %v", err)
	}

	if got := f.activeAccount(t, domain.CategoryGemini); got != "acc-trial" {
		t.Errorf("shared gemini pin should move to acc-trial, got %q", got)
	}
}

func TestFailover_RotatesToModelEligibleAccount(t *testing.T) {
	f := newTwoAccountPool(t, nil)

	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGemini, "acc-trial"); err != nil {
		t.Fatalf("pin gemini to trial: %v", err)
	}
	trial, err := f.repo.GetByID(f.ctx, "acc-trial")
	if err != nil {
		t.Fatalf("GetByID trial: %v", err)
	}

	engine := proxy.NewFailoverEngine(f.repo, nil, nil)
	_, _, next, rotErr := engine.HandleExhaustion(f.ctx, trial, "gemini-2.5-pro")
	if rotErr != nil {
		t.Fatalf("HandleExhaustion: %v", rotErr)
	}
	if next == nil || next.ID != "acc-paid" {
		t.Fatalf("expected rotation to acc-paid, got %+v", next)
	}
	if got := f.activeAccount(t, domain.CategoryGemini); got != "acc-paid" {
		t.Errorf("gemini pin should follow the rotation, got %q", got)
	}
}

func TestFailover_IgnoresIneligiblePinnedAccount(t *testing.T) {
	f := newTwoAccountPool(t, nil)

	// Another request pinned Claude/GPT to Trial, which cannot serve Opus 5.5.
	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryClaudeGPT, "acc-trial"); err != nil {
		t.Fatalf("pin claude_gpt to trial: %v", err)
	}
	paid, err := f.repo.GetByID(f.ctx, "acc-paid")
	if err != nil {
		t.Fatalf("GetByID paid: %v", err)
	}

	engine := proxy.NewFailoverEngine(f.repo, nil, nil)
	_, _, next, rotErr := engine.HandleExhaustion(f.ctx, paid, "claude-opus-5-5")

	if rotErr == nil {
		t.Fatalf("expected no eligible account error, got rotation to %+v", next)
	}
	if next != nil {
		t.Errorf("must not replay Opus 5.5 on an ineligible account, got %+v", next)
	}
	if got := f.activeAccount(t, domain.CategoryClaudeGPT); got != "acc-trial" {
		t.Errorf("claude_gpt pin must stay on acc-trial, got %q", got)
	}
}

func TestFailover_ArgonExhaustionRotatesSharedGemini(t *testing.T) {
	f := newTwoAccountPool(t, nil)

	f.seedModels(t, "acc-trial",
		&domain.ModelInfo{ID: "gemini-4-argon-medium", DisplayName: "Gemini 4 Argon (Medium)", Category: "gemini"},
	)

	f.seedModels(t, "acc-paid",
		&domain.ModelInfo{ID: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", Category: "gemini"},
	)
	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGemini, "acc-paid"); err != nil {
		t.Fatalf("pin gemini to paid: %v", err)
	}

	paid, err := f.repo.GetByID(f.ctx, "acc-paid")
	if err != nil {
		t.Fatalf("GetByID paid: %v", err)
	}

	engine := proxy.NewFailoverEngine(f.repo, nil, nil)
	_, _, next, rotErr := engine.HandleExhaustion(f.ctx, paid, "gemini-4-argon-medium")
	if rotErr != nil {
		t.Fatalf("HandleExhaustion(argon): %v", rotErr)
	}
	if next == nil || next.ID != "acc-trial" {
		t.Fatalf("expected argon traffic to rotate to acc-trial, got %+v", next)
	}

	if got := f.activeAccount(t, domain.CategoryGemini); got != "acc-trial" {
		t.Errorf("shared gemini pin should move to acc-trial, got %q", got)
	}
}

func TestArgonPreFlight_BlocksUnentitledAccount(t *testing.T) {
	// Account pool with only an unentitled trial account that has no Argon model and no Argon bucket.
	f := newEligibilityFixture(t, []*domain.Account{
		{ID: "acc-trial", Email: "trial@example.com", AccessToken: "trial-token", IsActive: true},
	}, nil)
	f.seedModels(t, "acc-trial",
		&domain.ModelInfo{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Category: "gemini"},
	)

	// An incoming Argon request with no Bearer header from client should receive 503 rather
	// than being sent upstream with trial-token.
	rec := f.requestModel(t, "gemini-4-argon-medium")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when no account has Argon entitlement, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestModelEligibility_SoloMode_BlocksCrossAccountDispatch(t *testing.T) {
	var requestedToken string
	responder := func(token, body string) (int, string) {
		requestedToken = token
		return http.StatusOK, `{"status":"ok"}`
	}

	f := newEligibilityFixture(t, []*domain.Account{
		{ID: "acc-solo", Email: "solo@example.com", AccessToken: "solo-token", IsActive: true},
		{ID: "acc-pool", Email: "pool@example.com", AccessToken: "pool-token", IsActive: false},
	}, responder)

	// Seed models: acc-solo has claude-3-5-haiku disabled, acc-pool has it enabled
	f.seedModels(t, "acc-solo", &domain.ModelInfo{ID: "claude-3-5-haiku", Category: "claude_gpt"})
	if err := f.repo.UpdateAccountModelRule(f.ctx, &domain.AccountModelRule{
		AccountID: "acc-solo",
		ModelID:   "claude-3-5-haiku",
		Enabled:   false,
	}); err != nil {
		t.Fatalf("disable model on solo account: %v", err)
	}

	f.seedModels(t, "acc-pool", &domain.ModelInfo{ID: "claude-3-5-haiku", Category: "claude_gpt"})

	// Pin to acc-solo in Solo mode
	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryClaudeGPT, "acc-solo"); err != nil {
		t.Fatalf("SetActiveForCategory: %v", err)
	}
	if err := f.repo.SetCategoryRoutingMode(f.ctx, domain.CategoryClaudeGPT, domain.RoutingModeSolo); err != nil {
		t.Fatalf("SetCategoryRoutingMode: %v", err)
	}

	// Requesting claude-3-5-haiku must NOT dispatch to acc-pool (pool-token)
	f.requestModel(t, "claude-3-5-haiku")
	if requestedToken == "pool-token" {
		t.Fatalf("Solo mode violation: request was rerouted to pool-token!")
	}
}

func TestProxy_ModelPriorityPin_OverridesCategoryBucket(t *testing.T) {
	f := newTwoAccountPool(t, nil)
	f.seedModels(t, "acc-trial",
		&domain.ModelInfo{ID: "gemini-3.8-flash-med", DisplayName: "Gemini 3.8 Flash (Med)", Category: "gemini"},
	)

	// Account paid is active for CategoryGemini; Account trial is pinned for gemini-3.8-flash-med.
	f.seedModels(t, "acc-paid",
		&domain.ModelInfo{ID: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", Category: "gemini"},
	)
	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGemini, "acc-paid"); err != nil {
		t.Fatalf("SetActiveForCategory: %v", err)
	}
	if err := f.repo.SetActiveForModels(f.ctx, "acc-trial", []string{"gemini-3.8-flash-med"}); err != nil {
		t.Fatalf("SetActiveForModels: %v", err)
	}

	// gemini-3.8-flash-med must route to acc-trial (model priority pin).
	recFlash := f.requestModel(t, "gemini-3.8-flash-med")
	if recFlash.Code != http.StatusOK {
		t.Fatalf("expected 200 for flash-med, got %d: %s", recFlash.Code, recFlash.Body.String())
	}
	if !strings.Contains(recFlash.Body.String(), "trial-token") {
		t.Errorf("expected gemini-3.8-flash-med to route to trial-token, got: %s", recFlash.Body.String())
	}

	// gemini-2.5-pro must still route to acc-paid (category bucket).
	recPro := f.requestModel(t, "gemini-2.5-pro")
	if recPro.Code != http.StatusOK {
		t.Fatalf("expected 200 for gemini-2.5-pro, got %d: %s", recPro.Code, recPro.Body.String())
	}
	if !strings.Contains(recPro.Body.String(), "paid-token") {
		t.Errorf("expected gemini-2.5-pro to route to paid-token, got: %s", recPro.Body.String())
	}
}

func TestProxy_ModelPriorityPin_FailsOverToCategoryActiveOn429AndPreservesPin(t *testing.T) {
	f := newTwoAccountPool(t, func(token, body string) (int, string) {
		if token == "trial-token" {
			return http.StatusTooManyRequests, `{"error":{"code":429,"message":"RESOURCE_EXHAUSTED","status":"RESOURCE_EXHAUSTED"}}`
		}
		return http.StatusOK, `{"token":"` + token + `"}`
	})

	f.seedModels(t, "acc-paid",
		&domain.ModelInfo{ID: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", Category: "gemini"},
	)
	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGemini, "acc-paid"); err != nil {
		t.Fatalf("SetActiveForCategory: %v", err)
	}
	if err := f.repo.SetActiveForModels(f.ctx, "acc-trial", []string{"gemini-3.8-flash"}); err != nil {
		t.Fatalf("SetActiveForModels: %v", err)
	}

	// First request hits 429 on acc-trial and replays on acc-paid.
	rec1 := f.requestModel(t, "gemini-3.8-flash")
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover, got %d: %s", rec1.Code, rec1.Body.String())
	}
	if !strings.Contains(rec1.Body.String(), "paid-token") {
		t.Errorf("expected failover to paid-token, got: %s", rec1.Body.String())
	}

	// CategoryGemini must remain pinned to acc-paid.
	if got := f.activeAccount(t, domain.CategoryGemini); got != "acc-paid" {
		t.Errorf("expected CategoryGemini to remain acc-paid, got %q", got)
	}

	// Model pin must remain saved on acc-trial for post-reset recovery.
	pins, err := f.repo.GetActiveModelsByAccount(f.ctx)
	if err != nil {
		t.Fatalf("GetActiveModelsByAccount: %v", err)
	}
	if len(pins["acc-trial"]) != 1 || pins["acc-trial"][0] != "gemini-3.8-flash" {
		t.Errorf("expected model pin preserved on acc-trial, got %+v", pins)
	}

	// Subsequent request while acc-trial is in cooldown must route directly to acc-paid.
	rec2 := f.requestModel(t, "gemini-3.8-flash")
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 on second request, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "paid-token") {
		t.Errorf("expected second request to route to paid-token, got: %s", rec2.Body.String())
	}
}
