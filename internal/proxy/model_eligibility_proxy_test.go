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
		&domain.ModelInfo{ID: "gemini-4-argon-medium", DisplayName: "Gemini 4 Argon (Medium)", Category: "gemini_argon"},
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

func TestGeminiArgonExhaustion_DoesNotLockOutRegularGemini(t *testing.T) {
	f := newTwoAccountPool(t, nil)

	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGeminiArgon, "acc-paid"); err != nil {
		t.Fatalf("pin argon to paid: %v", err)
	}

	// Argon weekly bucket exhausted on paid; regular gemini buckets stay healthy.
	reset := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	if _, err := f.db.ExecContext(f.ctx, `
		INSERT INTO quota_buckets (account_id, bucket_id, display_name, window, remaining_fraction, reset_time, updated_at)
		VALUES ('acc-paid', 'argon-weekly', 'Gemini Argon (weekly)', 'weekly', 0.0, ?, ?)
	`, reset, reset); err != nil {
		t.Fatalf("seed argon bucket: %v", err)
	}
	if _, err := f.db.ExecContext(f.ctx, `
		INSERT INTO quota_buckets (account_id, bucket_id, display_name, window, remaining_fraction, reset_time, updated_at)
		VALUES ('acc-paid', 'gemini-5h', 'Gemini (5h)', '5h', 1.0, ?, ?)
	`, reset, reset); err != nil {
		t.Fatalf("seed gemini bucket: %v", err)
	}

	// A regular Gemini request must still reach the paid account.
	rec := f.requestModel(t, "gemini-2.5-pro")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for regular gemini despite argon exhaustion, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "paid-token") {
		t.Errorf("argon exhaustion must not lock out regular gemini, got: %s", rec.Body.String())
	}
	if got := atomic.LoadInt64(f.hits); got != 1 {
		t.Errorf("expected exactly 1 upstream hit, got %d", got)
	}
}

func TestExhaustionOnArgon_MarksOnlyArgonExhausted(t *testing.T) {
	f := newTwoAccountPool(t, nil)

	// Seed Argon on trial so it has entitlement to receive the rotated argon category pin.
	f.seedModels(t, "acc-trial",
		&domain.ModelInfo{ID: "gemini-4-argon-medium", DisplayName: "Gemini 4 Argon (Medium)", Category: "gemini_argon"},
	)

	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGeminiArgon, "acc-paid"); err != nil {
		t.Fatalf("pin argon to paid: %v", err)
	}
	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGemini, "acc-paid"); err != nil {
		t.Fatalf("pin gemini to paid: %v", err)
	}

	paid, err := f.repo.GetByID(f.ctx, "acc-paid")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	engine := proxy.NewFailoverEngine(f.repo, nil, nil)
	if _, err := engine.RotateAccountForCategory(f.ctx, paid, domain.CategoryGeminiArgon); err != nil {
		t.Fatalf("RotateAccountForCategory(argon): %v", err)
	}

	if got := f.activeAccount(t, domain.CategoryGeminiArgon); got != "acc-trial" {
		t.Errorf("argon pin should move to the next account, got %q", got)
	}
	if got := f.activeAccount(t, domain.CategoryGemini); got != "acc-paid" {
		t.Errorf("regular gemini pin must stay on acc-paid after argon exhaustion, got %q", got)
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

func TestFailover_ArgonExhaustionIsolatesRegularGemini(t *testing.T) {
	f := newTwoAccountPool(t, nil)

	// Trial can serve Argon too, so the argon rotation has a valid destination.
	f.seedModels(t, "acc-trial",
		&domain.ModelInfo{ID: "gemini-4-argon-medium", DisplayName: "Gemini 4 Argon (Medium)", Category: "gemini_argon"},
	)

	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGeminiArgon, "acc-paid"); err != nil {
		t.Fatalf("pin argon to paid: %v", err)
	}
	if err := f.repo.SetActiveForCategory(f.ctx, domain.CategoryGemini, "acc-paid"); err != nil {
		t.Fatalf("pin gemini to paid: %v", err)
	}

	// The poller reports a healthy regular Gemini bucket on the paid account.
	reset := "2026-12-31T00:00:00Z"
	if _, err := f.db.ExecContext(f.ctx, `
		INSERT INTO quota_buckets (account_id, bucket_id, display_name, window, remaining_fraction, reset_time, updated_at)
		VALUES ('acc-paid', 'gemini-5h', 'Gemini (5h)', '5h', 0.8, ?, ?)
	`, reset, reset); err != nil {
		t.Fatalf("seed gemini bucket: %v", err)
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

	// Only the argon category moved: regular Gemini keeps using the paid account.
	if got := f.activeAccount(t, domain.CategoryGeminiArgon); got != "acc-trial" {
		t.Errorf("argon pin should move to acc-trial, got %q", got)
	}
	if got := f.activeAccount(t, domain.CategoryGemini); got != "acc-paid" {
		t.Errorf("gemini pin must stay on acc-paid, got %q", got)
	}
	available, err := f.repo.IsAccountAvailableForCategory(f.ctx, "acc-paid", domain.CategoryGemini)
	if err != nil {
		t.Fatalf("IsAccountAvailableForCategory(gemini): %v", err)
	}
	if !available {
		t.Error("acc-paid must stay available for regular gemini after argon exhaustion")
	}
	available, err = f.repo.IsAccountAvailableForCategory(f.ctx, "acc-paid", domain.CategoryGeminiArgon)
	if err != nil {
		t.Fatalf("IsAccountAvailableForCategory(argon): %v", err)
	}
	if available {
		t.Error("acc-paid must be unavailable for argon after argon exhaustion")
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
