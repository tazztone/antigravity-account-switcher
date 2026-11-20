package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/config"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/oauth"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/store/sqlite"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/web"
)

func setupWebTestServer(t *testing.T, opts ...web.Option) (*web.Server, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	accRepo := sqlite.NewAccountRepository(db)
	quotaRepo := sqlite.NewQuotaRepository(db)
	eventRepo := sqlite.NewEventRepository(db)
	srv, err := web.NewServer(accRepo, quotaRepo, nil, nil, eventRepo, nil, opts...)
	if err != nil {
		t.Fatalf("web.NewServer: %v", err)
	}
	return srv, db
}

func TestWeb_ConfigRollbackOnValidationError(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelPrimary = "gemini-2.5-pro"
	cfg.ModelSecondary = "claude-sonnet-4-6"
	srv, db := setupWebTestServer(t, web.WithConfig(cfg))
	defer db.Close()

	// Invalid update: identical primary and secondary with fallback enabled
	body := `{"model_primary":"gemini-2.5-pro","model_secondary":"gemini-2.5-pro","fallback_secondary_enabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/config invalid: got %d, want 400", rec.Code)
	}

	// Verify in-memory config was NOT corrupted
	getReq := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /api/config: got %d", getRec.Code)
	}
	var got struct {
		ModelSecondary string `json:"model_secondary"`
	}
	_ = json.Unmarshal(getRec.Body.Bytes(), &got)
	if got.ModelSecondary != "claude-sonnet-4-6" {
		t.Errorf("config was corrupted after failed validation: got %q, want claude-sonnet-4-6", got.ModelSecondary)
	}
}

func TestWeb_CSRFOriginGuard(t *testing.T) {
	srv, db := setupWebTestServer(t, web.WithShutdownFunc(func() {}))
	defer db.Close()

	cases := []struct {
		name       string
		origin     string
		secSite    string
		wantStatus int
	}{
		{"evil origin", "https://evil.example.com", "", http.StatusForbidden},
		{"cross-site fetch", "", "cross-site", http.StatusForbidden},
		{"localhost origin", "http://localhost:8080", "", http.StatusOK},
		{"127.0.0.1 origin", "http://127.0.0.1:8080", "", http.StatusOK},
		{"CLI no origin", "", "", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.secSite != "" {
				req.Header.Set("Sec-Fetch-Site", tc.secSite)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("got status %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

type blockingOAuthEngine struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingOAuthEngine) StartLoopbackFlow(ctx context.Context, opener oauth.BrowserOpener, urlLogger func(string)) (*domain.Account, error) {
	if urlLogger != nil {
		urlLogger("http://127.0.0.1:9999/auth")
	}
	close(b.started)
	<-b.release
	return nil, context.Canceled
}
func (b *blockingOAuthEngine) BuildAuthURL(redirectURI, state, codeChallenge string) string {
	return ""
}
func (b *blockingOAuthEngine) HandleCallbackRequest(r *http.Request) (*domain.Account, error) {
	return nil, nil
}
func (b *blockingOAuthEngine) RefreshToken(ctx context.Context, refreshToken string) (*oauth.TokenResponse, error) {
	return nil, nil
}
func (b *blockingOAuthEngine) EnsureValidToken(ctx context.Context, acc *domain.Account, safetyMargin time.Duration) (*domain.Account, error) {
	return acc, nil
}

func TestWeb_OAuthStartMethodAndConcurrencyGuard(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	// GET -> 405
	srvNil, err := web.NewServer(sqlite.NewAccountRepository(db), sqlite.NewQuotaRepository(db), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("web.NewServer: %v", err)
	}
	getRec := httptest.NewRecorder()
	srvNil.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/oauth/start", nil))
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /oauth/start: got %d, want 405", getRec.Code)
	}

	// nil engine POST -> 501
	postNilRec := httptest.NewRecorder()
	srvNil.ServeHTTP(postNilRec, httptest.NewRequest(http.MethodPost, "/oauth/start", nil))
	if postNilRec.Code != http.StatusNotImplemented {
		t.Errorf("POST /oauth/start nil engine: got %d, want 501", postNilRec.Code)
	}

	// Concurrent POST -> 409 Conflict
	eng := &blockingOAuthEngine{started: make(chan struct{}), release: make(chan struct{})}
	srv, err := web.NewServer(sqlite.NewAccountRepository(db), sqlite.NewQuotaRepository(db), nil, nil, nil, eng)
	if err != nil {
		t.Fatalf("web.NewServer: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rec1 := httptest.NewRecorder()
		srv.ServeHTTP(rec1, httptest.NewRequest(http.MethodPost, "/oauth/start", nil))
	}()

	<-eng.started
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/oauth/start", nil))
	close(eng.release)
	wg.Wait()

	if rec2.Code != http.StatusConflict {
		t.Errorf("concurrent POST /oauth/start: got %d, want 409", rec2.Code)
	}
}

func TestWeb_HandleEventsHistoryAndMethodGuard(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	eventRepo := sqlite.NewEventRepository(db)
	_ = eventRepo.Record(context.Background(), &domain.ProxyEvent{
		Type:      domain.EventTypeAccountSwitched,
		Message:   "hist-msg",
		Timestamp: time.Now().UTC(),
	})

	srv, err := web.NewServer(sqlite.NewAccountRepository(db), sqlite.NewQuotaRepository(db), nil, nil, eventRepo, nil)
	if err != nil {
		t.Fatalf("web.NewServer: %v", err)
	}

	// POST /api/events -> 405
	postRec := httptest.NewRecorder()
	srv.ServeHTTP(postRec, httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(nil)))
	if postRec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/events: got %d, want 405", postRec.Code)
	}

	// GET /api/events -> replays event: history
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: history\n") || !strings.Contains(body, "hist-msg") {
		t.Errorf("expected event: history with hist-msg, got:\n%s", body)
	}
}

func TestWeb_GetAccountAndDeleteActiveAutoPromotes(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	accRepo := sqlite.NewAccountRepository(db)
	ctx := context.Background()
	_ = accRepo.Create(ctx, &domain.Account{ID: "acc-1", Email: "a1@example.com", Status: domain.AccountStatusActive, IsActive: true})
	_ = accRepo.Create(ctx, &domain.Account{ID: "acc-2", Email: "a2@example.com", Status: domain.AccountStatusActive, IsActive: false})

	srv, err := web.NewServer(accRepo, sqlite.NewQuotaRepository(db), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("web.NewServer: %v", err)
	}

	// GET /api/accounts/acc-1 -> 200
	rec1 := httptest.NewRecorder()
	srv.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1", nil))
	if rec1.Code != http.StatusOK {
		t.Errorf("GET /api/accounts/acc-1: got %d, want 200", rec1.Code)
	}

	// GET /api/accounts/nonexistent -> 404
	rec404 := httptest.NewRecorder()
	srv.ServeHTTP(rec404, httptest.NewRequest(http.MethodGet, "/api/accounts/nonexistent", nil))
	if rec404.Code != http.StatusNotFound {
		t.Errorf("GET /api/accounts/nonexistent: got %d, want 404", rec404.Code)
	}

	// DELETE /api/accounts/acc-1 -> 200 and auto-promotes acc-2
	delRec := httptest.NewRecorder()
	srv.ServeHTTP(delRec, httptest.NewRequest(http.MethodDelete, "/api/accounts/acc-1", nil))
	if delRec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/accounts/acc-1: got %d, want 200", delRec.Code)
	}
	active, err := accRepo.GetActive(ctx)
	if err != nil || active == nil || active.ID != "acc-2" {
		t.Errorf("expected acc-2 to be auto-promoted, got %+v, err=%v", active, err)
	}
}

func TestWeb_ServeStaticDirectoryDotFallback(t *testing.T) {
	srv, db := setupWebTestServer(t)
	defer db.Close()

	recDot := httptest.NewRecorder()
	srv.ServeHTTP(recDot, httptest.NewRequest(http.MethodGet, "/.", nil))
	if recDot.Code != http.StatusOK || recDot.Body.Len() == 0 {
		t.Errorf("GET /.: got status %d, body len %d", recDot.Code, recDot.Body.Len())
	}

	recFoo := httptest.NewRecorder()
	srv.ServeHTTP(recFoo, httptest.NewRequest(http.MethodGet, "/api/accountsfoo", nil))
	if strings.Contains(recFoo.Body.String(), "account not found") {
		t.Errorf("GET /api/accountsfoo should not route to HandleAccounts")
	}
}

func TestWeb_ConcurrentSetPollerAndFallbackSetter(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	accRepo := sqlite.NewAccountRepository(db)
	_ = accRepo.Create(context.Background(), &domain.Account{ID: "acc-1", Email: "a1@example.com", Status: domain.AccountStatusActive, IsActive: true})

	api := web.NewAPIHandler(accRepo, sqlite.NewQuotaRepository(db), nil, nil, nil, nil, "test")
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				api.SetPoller(nil)
			} else if i%3 == 1 {
				api.SetFallbackConfigSetter(nil)
			} else {
				rec := httptest.NewRecorder()
				api.HandleQuotaRefresh(rec, httptest.NewRequest(http.MethodPost, "/api/quota/refresh", nil))
			}
		}(i)
	}
	wg.Wait()
}

func TestWeb_ConversationsLimitClamping(t *testing.T) {
	srv, db := setupWebTestServer(t)
	defer db.Close()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/conversations?limit=9999", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/conversations?limit=9999: got %d, want 200", rec.Code)
	}

	recMetrics := httptest.NewRecorder()
	srv.ServeHTTP(recMetrics, httptest.NewRequest(http.MethodGet, "/api/metrics?account_id=acc-1&period=24h", nil))
	if recMetrics.Code != http.StatusOK {
		t.Errorf("GET /api/metrics: got %d, want 200", recMetrics.Code)
	}
}
