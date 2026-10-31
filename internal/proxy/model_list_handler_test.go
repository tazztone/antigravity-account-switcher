package proxy_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/proxy"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/store/sqlite"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/test/mocks"
)

const modelListPath = "/v1internal:fetchAvailableModels"

// modelListFixture wires a SQLite-backed account pool to a proxy handler whose
// upstream is the shared Google Cloud Code PA mock.
type modelListFixture struct {
	ctx     context.Context
	repo    *sqlite.AccountRepository
	mock    *mocks.MockGoogleServer
	handler *proxy.ProxyHandler
	server  *httptest.Server
}

func newModelListFixture(t *testing.T, accounts []*domain.Account, opts ...proxy.Option) *modelListFixture {
	t.Helper()

	db, err := sqlite.Open(filepath.Join(t.TempDir(), "model_list.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	repo := sqlite.NewAccountRepository(db)
	f := &modelListFixture{
		ctx:  ctx,
		repo: repo,
		mock: mocks.NewMockGoogleServer(),
	}
	t.Cleanup(f.mock.Close)

	f.addAccounts(t, accounts)

	handlerOpts := append([]proxy.Option{proxy.WithTargetURL(f.mock.URL)}, opts...)
	handler, err := proxy.NewProxyHandler(repo, handlerOpts...)
	if err != nil {
		t.Fatalf("NewProxyHandler: %v", err)
	}
	f.handler = handler
	f.server = httptest.NewServer(handler)
	t.Cleanup(f.server.Close)
	return f
}

// addAccounts inserts accounts into the pool, defaulting their status to active.
func (f *modelListFixture) addAccounts(t *testing.T, accounts []*domain.Account) {
	t.Helper()
	for _, acc := range accounts {
		if acc.Status == "" {
			acc.Status = domain.AccountStatusActive
		}
		if err := f.repo.Create(f.ctx, acc); err != nil {
			t.Fatalf("Create %s: %v", acc.ID, err)
		}
	}
}

// fetchModels calls the proxy model catalog endpoint and decodes the merged catalog.
func (f *modelListFixture) fetchModels(t *testing.T, header map[string]string) (*http.Response, mocks.FetchAvailableModelsResponse) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, f.server.URL+modelListPath, strings.NewReader(`{"project":"aicode-consumers"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range header {
		req.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("model list request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read model list response: %v", err)
	}

	var payload mocks.FetchAvailableModelsResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode model list response: %v (%s)", err, body)
		}
	}
	return resp, payload
}

// upstreamBearers returns the bearer tokens used for model catalog requests, sorted.
func (f *modelListFixture) upstreamBearers() []string {
	var bearers []string
	for _, req := range f.mock.RequestsForPath(modelListPath) {
		bearers = append(bearers, req.AuthBearer)
	}
	sort.Strings(bearers)
	return bearers
}

func modelIDs(models map[string]mocks.ModelPayloadItem) []string {
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func newClaudeAccount() *domain.Account {
	return &domain.Account{ID: "acc-claude", Email: "claude@example.com", AccessToken: "token-claude", IsActive: true}
}

func newGeminiAccount() *domain.Account {
	return &domain.Account{ID: "acc-gemini", Email: "gemini@example.com", AccessToken: "token-gemini"}
}

// configureTwoPlanPool gives the Claude account Sonnet 4.6 and the Gemini account
// Gemini 2.5 Pro, mirroring two accounts with different entitlements.
func configureTwoPlanPool(f *modelListFixture) {
	f.mock.SetAccountModels("token-claude", "claude-sonnet-4-6",
		map[string]mocks.ModelPayloadItem{
			"claude-sonnet-4-6": {DisplayName: "Claude Sonnet 4.6", Recommended: true, SupportsThinking: true},
		},
		mocks.NewAgentModelSort("Recommended", "claude-sonnet-4-6"),
	)
	f.mock.SetAccountModels("token-gemini", "gemini-2.5-pro",
		map[string]mocks.ModelPayloadItem{
			"gemini-2.5-pro": {DisplayName: "Gemini 2.5 Pro", Recommended: true},
		},
		mocks.NewAgentModelSort("Recommended", "gemini-2.5-pro"),
	)
}

func TestModelListHandler_UnionsModelsAcrossPool(t *testing.T) {
	f := newModelListFixture(t, []*domain.Account{newClaudeAccount(), newGeminiAccount()})
	configureTwoPlanPool(f)

	resp, payload := f.fetchModels(t, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", contentType)
	}

	want := []string{"claude-sonnet-4-6", "gemini-2.5-pro"}
	if got := modelIDs(payload.Models); !equalStrings(got, want) {
		t.Fatalf("merged models = %v, want %v", got, want)
	}
	if payload.DefaultAgentModelID != "claude-sonnet-4-6" {
		t.Errorf("defaultAgentModelId = %q, want the active account's default", payload.DefaultAgentModelID)
	}

	if got := f.upstreamBearers(); !equalStrings(got, []string{"token-claude", "token-gemini"}) {
		t.Errorf("upstream bearers = %v, want one request per account", got)
	}

	for _, recorded := range f.mock.RequestsForPath(modelListPath) {
		if got := recorded.Header.Get("Accept-Encoding"); got != "identity" {
			t.Errorf("Accept-Encoding = %q, want identity so the catalog stays mergeable", got)
		}
	}
}

func TestModelListHandler_OrdersEveryMergedModel(t *testing.T) {
	f := newModelListFixture(t, []*domain.Account{newClaudeAccount(), newGeminiAccount()})
	configureTwoPlanPool(f)

	_, payload := f.fetchModels(t, nil)

	var ordered []string
	for _, sort := range payload.AgentModelSorts {
		for _, group := range sort.Groups {
			ordered = append(ordered, group.ModelIDs...)
		}
	}
	if len(ordered) != 2 {
		t.Fatalf("ordered models = %v, want both merged models", ordered)
	}
	seen := map[string]bool{}
	for _, id := range ordered {
		seen[id] = true
	}
	for _, id := range []string{"claude-sonnet-4-6", "gemini-2.5-pro"} {
		if !seen[id] {
			t.Errorf("model %q missing from agentModelSorts: %v", id, ordered)
		}
	}
}

func TestModelListHandler_ExcludesDisabledAccounts(t *testing.T) {
	f := newModelListFixture(t, []*domain.Account{
		newClaudeAccount(),
		newGeminiAccount(),
		{ID: "acc-disabled", Email: "off@example.com", AccessToken: "token-off", Status: domain.AccountStatusDisabled},
	})
	configureTwoPlanPool(f)
	f.mock.SetAccountModels("token-off", "gpt-oss-120b", map[string]mocks.ModelPayloadItem{
		"gpt-oss-120b": {DisplayName: "GPT-OSS 120B"},
	})

	_, payload := f.fetchModels(t, nil)

	if got := modelIDs(payload.Models); !equalStrings(got, []string{"claude-sonnet-4-6", "gemini-2.5-pro"}) {
		t.Errorf("merged models = %v, want the disabled account excluded", got)
	}
	for _, bearer := range f.upstreamBearers() {
		if bearer == "token-off" {
			t.Error("disabled account received a model list request")
		}
	}
}

func TestModelListHandler_ServesCachedList(t *testing.T) {
	f := newModelListFixture(t, []*domain.Account{newClaudeAccount(), newGeminiAccount()})
	configureTwoPlanPool(f)

	first, firstPayload := f.fetchModels(t, nil)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first call status = %d, want 200", first.StatusCode)
	}
	callsAfterFirst := f.mock.RequestCount()

	second, secondPayload := f.fetchModels(t, nil)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second call status = %d, want 200", second.StatusCode)
	}
	if got := f.mock.RequestCount(); got != callsAfterFirst {
		t.Errorf("upstream calls = %d after cache hit, want %d", got, callsAfterFirst)
	}
	if !equalStrings(modelIDs(secondPayload.Models), modelIDs(firstPayload.Models)) {
		t.Errorf("cached models = %v, want %v", modelIDs(secondPayload.Models), modelIDs(firstPayload.Models))
	}
}

func TestModelListHandler_InvalidatesCacheWhenPoolChanges(t *testing.T) {
	f := newModelListFixture(t, []*domain.Account{newClaudeAccount(), newGeminiAccount()})
	configureTwoPlanPool(f)

	if _, payload := f.fetchModels(t, nil); len(payload.Models) != 2 {
		t.Fatalf("initial merged models = %v, want 2", modelIDs(payload.Models))
	}
	callsAfterFirst := f.mock.RequestCount()

	f.addAccounts(t, []*domain.Account{
		{ID: "acc-argon", Email: "argon@example.com", AccessToken: "token-argon"},
	})
	f.mock.SetAccountModels("token-argon", "gemini-4-argon-high",
		map[string]mocks.ModelPayloadItem{"gemini-4-argon-high": {DisplayName: "Gemini 4 Argon (High)"}},
		mocks.NewAgentModelSort("Recommended", "gemini-4-argon-high"),
	)

	_, payload := f.fetchModels(t, nil)
	if got := f.mock.RequestCount(); got != callsAfterFirst+3 {
		t.Errorf("upstream calls = %d after pool growth, want %d (cache must be invalidated)", got, callsAfterFirst+3)
	}
	want := []string{"claude-sonnet-4-6", "gemini-2.5-pro", "gemini-4-argon-high"}
	if got := modelIDs(payload.Models); !equalStrings(got, want) {
		t.Errorf("merged models = %v, want %v", got, want)
	}
}

func TestModelListHandler_ToleratesFailingAccount(t *testing.T) {
	f := newModelListFixture(t, []*domain.Account{newClaudeAccount(), newGeminiAccount()})
	configureTwoPlanPool(f)
	f.mock.ConfigureAccount("token-gemini", &mocks.AccountBehavior{
		ForceStatusCode: http.StatusInternalServerError,
		ForceErrorCode:  "INTERNAL",
	})

	resp, payload := f.fetchModels(t, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the healthy account", resp.StatusCode)
	}
	if got := modelIDs(payload.Models); !equalStrings(got, []string{"claude-sonnet-4-6"}) {
		t.Errorf("merged models = %v, want only the healthy account's models", got)
	}

	// Partial responses must not be cached: once the failing account recovers, the next
	// request must fan out again and include all models.
	f.mock.ConfigureAccount("token-gemini", &mocks.AccountBehavior{
		AvailableModels: map[string]mocks.ModelPayloadItem{
			"gemini-2.5-pro": {DisplayName: "Gemini 2.5 Pro", Recommended: true},
		},
		DefaultAgentModelID: "gemini-2.5-pro",
		AgentModelSorts: []mocks.AgentModelSort{
			mocks.NewAgentModelSort("Recommended", "gemini-2.5-pro"),
		},
	})
	_, recoveredPayload := f.fetchModels(t, nil)
	if got := modelIDs(recoveredPayload.Models); !equalStrings(got, []string{"claude-sonnet-4-6", "gemini-2.5-pro"}) {
		t.Errorf("merged models after recovery = %v, want full union %v", got, []string{"claude-sonnet-4-6", "gemini-2.5-pro"})
	}
}

func TestModelListHandler_InvalidatesCacheWhenActiveAccountChanges(t *testing.T) {
	claudeAcc := newClaudeAccount()
	geminiAcc := newGeminiAccount()
	f := newModelListFixture(t, []*domain.Account{claudeAcc, geminiAcc})
	configureTwoPlanPool(f)

	_, firstPayload := f.fetchModels(t, nil)
	if firstPayload.DefaultAgentModelID != "claude-sonnet-4-6" {
		t.Fatalf("initial defaultAgentModelId = %q, want claude-sonnet-4-6", firstPayload.DefaultAgentModelID)
	}

	// Switch active account to Gemini
	if err := f.repo.SetActive(f.ctx, geminiAcc.ID); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	_, secondPayload := f.fetchModels(t, nil)
	if secondPayload.DefaultAgentModelID != "gemini-2.5-pro" {
		t.Errorf("defaultAgentModelId after active switch = %q, want gemini-2.5-pro", secondPayload.DefaultAgentModelID)
	}
}

func TestModelListHandler_RefreshesOn401Unauthorized(t *testing.T) {
	f := newModelListFixture(t, []*domain.Account{
		{
			ID:           "acc-claude",
			Email:        "claude@example.com",
			AccessToken:  "revoked-token",
			RefreshToken: "refresh-claude",
			TokenExpiry:  time.Now().Add(time.Hour), // looks valid, but revoked upstream
			IsActive:     true,
		},
	}, proxy.WithTokenRefresher(proxy.TokenRefresherFunc(
		func(ctx context.Context, refreshToken string) (string, time.Time, error) {
			return "fresh-token-401", time.Now().Add(time.Hour), nil
		},
	)))

	f.mock.ConfigureAccount("revoked-token", &mocks.AccountBehavior{
		ForceStatusCode: http.StatusUnauthorized,
		ForceErrorCode:  "UNAUTHENTICATED",
	})
	f.mock.SetAccountModels("fresh-token-401", "claude-sonnet-4-6",
		map[string]mocks.ModelPayloadItem{"claude-sonnet-4-6": {DisplayName: "Claude Sonnet 4.6"}},
	)

	resp, payload := f.fetchModels(t, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after 401 refresh", resp.StatusCode)
	}
	if got := modelIDs(payload.Models); !equalStrings(got, []string{"claude-sonnet-4-6"}) {
		t.Errorf("merged models = %v, want refreshed account models", got)
	}

	stored, err := f.repo.GetByID(f.ctx, "acc-claude")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.AccessToken != "fresh-token-401" {
		t.Errorf("stored token = %q, want fresh-token-401", stored.AccessToken)
	}
}

func TestModelListHandler_HonoursPerAccountTimeout(t *testing.T) {
	f := newModelListFixture(t, []*domain.Account{newClaudeAccount(), newGeminiAccount()},
		proxy.WithModelListTimeout(120*time.Millisecond))
	configureTwoPlanPool(f)
	f.mock.ConfigureAccount("token-gemini", &mocks.AccountBehavior{
		CustomHandler: func(w http.ResponseWriter, r *http.Request) bool {
			if strings.HasSuffix(r.URL.Path, "fetchAvailableModels") {
				time.Sleep(400 * time.Millisecond)
			}
			return false
		},
	})

	resp, payload := f.fetchModels(t, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the responsive account", resp.StatusCode)
	}
	if got := modelIDs(payload.Models); !equalStrings(got, []string{"claude-sonnet-4-6"}) {
		t.Errorf("merged models = %v, want the timed-out account excluded", got)
	}
}

func TestModelListHandler_RefreshesExpiredTokens(t *testing.T) {
	f := newModelListFixture(t, []*domain.Account{
		{
			ID:           "acc-claude",
			Email:        "claude@example.com",
			AccessToken:  "stale-token",
			RefreshToken: "refresh-claude",
			TokenExpiry:  time.Now().Add(-time.Hour),
			IsActive:     true,
		},
	}, proxy.WithTokenRefresher(proxy.TokenRefresherFunc(
		func(ctx context.Context, refreshToken string) (string, time.Time, error) {
			return "fresh-token", time.Now().Add(time.Hour), nil
		},
	)))
	f.mock.SetAccountModels("fresh-token", "claude-sonnet-4-6",
		map[string]mocks.ModelPayloadItem{"claude-sonnet-4-6": {DisplayName: "Claude Sonnet 4.6"}},
	)

	_, payload := f.fetchModels(t, nil)

	if got := modelIDs(payload.Models); !equalStrings(got, []string{"claude-sonnet-4-6"}) {
		t.Errorf("merged models = %v, want the refreshed account's models", got)
	}
	if got := f.upstreamBearers(); !equalStrings(got, []string{"fresh-token"}) {
		t.Errorf("upstream bearers = %v, want the refreshed token", got)
	}

	stored, err := f.repo.GetByID(f.ctx, "acc-claude")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.AccessToken != "fresh-token" {
		t.Errorf("stored access token = %q, want the refreshed token persisted", stored.AccessToken)
	}
}

func TestModelListHandler_PersistsDiscoveredModels(t *testing.T) {
	f := newModelListFixture(t, []*domain.Account{newClaudeAccount(), newGeminiAccount()})
	configureTwoPlanPool(f)

	if _, payload := f.fetchModels(t, nil); len(payload.Models) != 2 {
		t.Fatalf("merged models = %v, want 2", modelIDs(payload.Models))
	}

	for accountID, wantModel := range map[string]string{
		"acc-claude": "claude-sonnet-4-6",
		"acc-gemini": "gemini-2.5-pro",
	} {
		rules, err := f.repo.ListAccountModelRules(f.ctx, accountID)
		if err != nil {
			t.Fatalf("ListAccountModelRules %s: %v", accountID, err)
		}
		found := false
		for _, rule := range rules {
			if rule.ModelID == wantModel {
				found = true
				if !rule.Enabled {
					t.Errorf("rule %s on %s is disabled, want enabled", wantModel, accountID)
				}
			}
		}
		if !found {
			t.Errorf("model %q was not persisted for %s: %v", wantModel, accountID, rules)
		}
	}
}

func TestModelListHandler_FallsBackWithoutPool(t *testing.T) {
	f := newModelListFixture(t, nil)

	resp, _ := f.fetchModels(t, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want the existing 503 pool-unavailable path", resp.StatusCode)
	}
	if calls := len(f.mock.RequestsForPath(modelListPath)); calls != 0 {
		t.Errorf("upstream model list calls = %d, want the normal proxy path instead", calls)
	}
}

func TestModelListHandler_PassesThroughClientTokenWithoutPool(t *testing.T) {
	f := newModelListFixture(t, nil)

	resp, payload := f.fetchModels(t, map[string]string{"Authorization": "Bearer client-token"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the passthrough path", resp.StatusCode)
	}
	if got := modelIDs(payload.Models); len(got) == 0 {
		t.Error("passthrough response carried no models")
	}
	if got := f.upstreamBearers(); !equalStrings(got, []string{"client-token"}) {
		t.Errorf("upstream bearers = %v, want the client's own token", got)
	}
}

// equalStrings reports sorted-slice equality.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
