package quota

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/store/sqlite"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/test/mocks"
)

type mockBroadcaster struct {
	mu     sync.Mutex
	events []*domain.ProxyEvent
}

func (m *mockBroadcaster) Broadcast(event *domain.ProxyEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *mockBroadcaster) Subscribe() (<-chan *domain.ProxyEvent, func()) {
	ch := make(chan *domain.ProxyEvent, 10)
	return ch, func() {}
}

func (m *mockBroadcaster) getEvents() []*domain.ProxyEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]*domain.ProxyEvent, len(m.events))
	copy(copied, m.events)
	return copied
}

func setupTestStore(t *testing.T) (*sqlite.DB, *sqlite.AccountRepository, *sqlite.QuotaRepository) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test_quota.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	accRepo := sqlite.NewAccountRepository(db)
	quotaRepo := sqlite.NewQuotaRepository(db)
	return db, accRepo, quotaRepo
}

func TestPoller_PollAccount_Success(t *testing.T) {
	server := mocks.NewMockGoogleServer()
	defer server.Close()

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:          "acc-1",
		Email:       "acc1@example.com",
		AccessToken: "token-1",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusActive,
		IsActive:    true,
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	buckets, err := p.PollAccount(ctx, "acc-1")
	if err != nil {
		t.Fatalf("PollAccount failed: %v", err)
	}

	if len(buckets) == 0 {
		t.Fatal("expected quota buckets, got 0")
	}

	// Verify buckets in SQLite
	stored, err := quotaRepo.GetByAccountID(ctx, "acc-1")
	if err != nil {
		t.Fatalf("GetByAccountID failed: %v", err)
	}
	if len(stored) != len(buckets) {
		t.Errorf("stored buckets count %d != returned count %d", len(stored), len(buckets))
	}

	// Verify bucket details
	foundPro := false
	for _, b := range stored {
		if b.BucketID == "gemini-2.5-pro" {
			foundPro = true
			if b.RemainingFraction != 0.75 {
				t.Errorf("expected 0.75 remaining fraction, got %f", b.RemainingFraction)
			}
			if b.RemainingAmount != 750 {
				t.Errorf("expected 750 remaining amount, got %d", b.RemainingAmount)
			}
		}
	}
	if !foundPro {
		t.Error("bucket gemini-2.5-pro not found in stored buckets")
	}
}

func TestPoller_TokenRefresh_OnExpired(t *testing.T) {
	server := mocks.NewMockGoogleServer()
	defer server.Close()

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	acc := &domain.Account{
		ID:           "acc-expired",
		Email:        "expired@example.com",
		AccessToken:  "old-token",
		RefreshToken: "refresh-token-1",
		TokenExpiry:  now.Add(-10 * time.Minute), // Expired
		Status:       domain.AccountStatusActive,
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	refreshed := false
	refresher := TokenRefresherFunc(func(ctx context.Context, rt string) (string, time.Time, error) {
		if rt != "refresh-token-1" {
			t.Errorf("unexpected refresh token: %s", rt)
		}
		refreshed = true
		return "fresh-access-token", now.Add(1 * time.Hour), nil
	})

	broadcaster := &mockBroadcaster{}

	p, err := NewPoller(accRepo, quotaRepo,
		WithBaseURL(server.URL),
		WithTokenRefresher(refresher),
		WithEventBroadcaster(broadcaster),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}

	if !refreshed {
		t.Fatal("expected token refresh to be triggered, but was not")
	}

	// Verify updated token in SQLite
	updated, err := accRepo.GetByID(ctx, "acc-expired")
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if updated.AccessToken != "fresh-access-token" {
		t.Errorf("expected AccessToken to be fresh-access-token, got %s", updated.AccessToken)
	}

	// Check event
	events := broadcaster.getEvents()
	foundTokenEvent := false
	for _, ev := range events {
		if ev.Type == domain.EventTypeTokenRefreshed && ev.AccountID == "acc-expired" {
			foundTokenEvent = true
		}
	}
	if !foundTokenEvent {
		t.Error("expected EventTypeTokenRefreshed event")
	}
}

func TestPoller_Unauthorized_ForceRefresh(t *testing.T) {
	// Custom HTTP server returning 401 on "Bearer old-token" and 200 on "Bearer valid-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "Bearer old-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if auth == "Bearer valid-token" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"groups":[{"displayName":"test","buckets":[{"bucketId":"gemini-pro","window":"DAILY","remainingFraction":0.9}]}]}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	acc := &domain.Account{
		ID:           "acc-unauth",
		Email:        "unauth@example.com",
		AccessToken:  "old-token",
		RefreshToken: "refresh-token-unauth",
		TokenExpiry:  now.Add(1 * time.Hour), // Not expired locally, but server rejects with 401
		Status:       domain.AccountStatusActive,
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	refreshed := false
	refresher := TokenRefresherFunc(func(ctx context.Context, rt string) (string, time.Time, error) {
		refreshed = true
		return "valid-token", now.Add(1 * time.Hour), nil
	})

	p, err := NewPoller(accRepo, quotaRepo,
		WithBaseURL(server.URL),
		WithTokenRefresher(refresher),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}

	if !refreshed {
		t.Fatal("expected forced token refresh on 401")
	}

	updated, err := accRepo.GetByID(ctx, "acc-unauth")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.AccessToken != "valid-token" {
		t.Errorf("expected AccessToken updated to valid-token, got %s", updated.AccessToken)
	}
	if updated.Status != domain.AccountStatusActive {
		t.Errorf("expected status active, got %s", updated.Status)
	}
}

func TestPoller_RevokedToken_MarksError(t *testing.T) {
	server := mocks.NewMockGoogleServer()
	defer server.Close()

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	acc := &domain.Account{
		ID:           "acc-revoked",
		Email:        "revoked@example.com",
		AccessToken:  "revoked-access",
		RefreshToken: "revoked-refresh",
		TokenExpiry:  now.Add(-10 * time.Minute),
		Status:       domain.AccountStatusActive,
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	refresher := TokenRefresherFunc(func(ctx context.Context, rt string) (string, time.Time, error) {
		return "", time.Time{}, errors.New("invalid_grant: Token has been expired or revoked.")
	})

	p, err := NewPoller(accRepo, quotaRepo,
		WithBaseURL(server.URL),
		WithTokenRefresher(refresher),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	_ = p.PollOnce(ctx)

	updated, err := accRepo.GetByID(ctx, "acc-revoked")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Status != domain.AccountStatusError {
		t.Errorf("expected status %s, got %s", domain.AccountStatusError, updated.Status)
	}
}

func TestPoller_AutoRestore_OnRemainingFraction(t *testing.T) {
	server := mocks.NewMockGoogleServer()
	defer server.Close()

	// Server returns restored quota (fraction > 0)
	server.SetAccountQuota("token-restore", []mocks.QuotaSummaryBucket{
		{
			BucketID:          "gemini-2.5-pro",
			DisplayName:       "Gemini 2.5 Pro",
			RemainingFraction: 0.85,
			RemainingAmount:   850,
			ResetTime:         time.Now().Add(12 * time.Hour),
		},
	})

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:          "acc-restore",
		Email:       "restore@example.com",
		AccessToken: "token-restore",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusExhausted, // Initially exhausted
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("create account: %v", err)
	}

	broadcaster := &mockBroadcaster{}
	p, err := NewPoller(accRepo, quotaRepo,
		WithBaseURL(server.URL),
		WithEventBroadcaster(broadcaster),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}

	// Status must be restored to active!
	updated, err := accRepo.GetByID(ctx, "acc-restore")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Status != domain.AccountStatusActive {
		t.Errorf("expected account status active after restore, got %s", updated.Status)
	}

	// Verify EventTypeQuotaRestored event
	events := broadcaster.getEvents()
	foundRestoreEvent := false
	for _, ev := range events {
		if ev.Type == domain.EventTypeQuotaRestored && ev.AccountID == "acc-restore" {
			foundRestoreEvent = true
		}
	}
	if !foundRestoreEvent {
		t.Error("expected EventTypeQuotaRestored event to be broadcast")
	}
}

func TestPoller_AutoRestore_OnResetTimeElapsed(t *testing.T) {
	server := mocks.NewMockGoogleServer()
	defer server.Close()

	past := time.Now().Add(-1 * time.Hour)
	server.SetAccountQuota("token-past-reset", []mocks.QuotaSummaryBucket{
		{
			BucketID:          "gemini-2.5-pro",
			DisplayName:       "Gemini 2.5 Pro",
			RemainingFraction: 0.0, // fraction is 0, but resetTime is in past!
			ResetTime:         past,
		},
	})

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:          "acc-past-reset",
		Email:       "pastreset@example.com",
		AccessToken: "token-past-reset",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusExhausted,
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("create account: %v", err)
	}

	p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}

	updated, err := accRepo.GetByID(ctx, "acc-past-reset")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Status != domain.AccountStatusActive {
		t.Errorf("expected account status active after reset time passed, got %s", updated.Status)
	}
}

func TestPoller_NoAutoRestore_WhileStillExhausted(t *testing.T) {
	server := mocks.NewMockGoogleServer()
	defer server.Close()

	future := time.Now().Add(2 * time.Hour)
	server.SetAccountQuota("token-still-exhausted", []mocks.QuotaSummaryBucket{
		{
			BucketID:          "gemini-2.5-pro",
			DisplayName:       "Gemini 2.5 Pro",
			RemainingFraction: 0.0,
			ResetTime:         future,
		},
	})

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:          "acc-still-exhausted",
		Email:       "still@example.com",
		AccessToken: "token-still-exhausted",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusExhausted,
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("create account: %v", err)
	}

	p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}

	updated, err := accRepo.GetByID(ctx, "acc-still-exhausted")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Status != domain.AccountStatusExhausted {
		t.Errorf("expected account status to remain exhausted, got %s", updated.Status)
	}
}

func TestPoller_DatabaseLevel_AutoRestorePastReset(t *testing.T) {
	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	past := time.Now().Add(-10 * time.Minute)
	acc := &domain.Account{
		ID:          "acc-db-reset",
		Email:       "dbreset@example.com",
		AccessToken: "token-db-reset",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusExhausted,
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("create account: %v", err)
	}

	// Insert bucket with reset_time in the past
	if err := quotaRepo.UpsertBuckets(ctx, []*domain.QuotaBucket{
		{
			AccountID:         "acc-db-reset",
			BucketID:          "gemini-2.5-pro",
			DisplayName:       "Gemini 2.5 Pro",
			Window:            domain.QuotaWindowDaily,
			RemainingFraction: 0.0,
			ResetTime:         past,
		},
	}); err != nil {
		t.Fatalf("UpsertBuckets: %v", err)
	}

	// Create a dummy HTTP server that returns error to prove Prong 1 works from DB alone
	dummyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dummyServer.Close()

	p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(dummyServer.URL))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	// PollOnce runs Prong 1 before HTTP calls
	_ = p.PollOnce(ctx)

	updated, err := accRepo.GetByID(ctx, "acc-db-reset")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Status != domain.AccountStatusActive {
		t.Errorf("expected status active via DB-level reset time elapsed, got %s", updated.Status)
	}
}

func TestPoller_Fallback_LegacyRetrieveUserQuota(t *testing.T) {
	// Custom HTTP handler returning 404 for summary, and 200 for legacy
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1internal:retrieveUserQuotaSummary" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/v1internal:retrieveUserQuota" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"buckets": [
					{
						"model_id": "legacy-gemini-pro",
						"remaining_fraction": 0.5,
						"remaining_amount": 50,
						"reset_time": "2026-09-03T12:00:00Z"
					}
				]
			}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:          "acc-legacy",
		Email:       "legacy@example.com",
		AccessToken: "legacy-token",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusActive,
	}
	_ = accRepo.Create(ctx, acc)

	p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	buckets, err := p.PollAccount(ctx, "acc-legacy")
	if err != nil {
		t.Fatalf("PollAccount with fallback failed: %v", err)
	}

	if len(buckets) != 1 || buckets[0].BucketID != "legacy-gemini-pro" {
		t.Errorf("unexpected legacy fallback buckets: %+v", buckets)
	}
}

func TestPoller_DaemonLifecycle_GracefulShutdown(t *testing.T) {
	server := mocks.NewMockGoogleServer()
	defer server.Close()

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	p, err := NewPoller(accRepo, quotaRepo,
		WithBaseURL(server.URL),
		WithPollInterval(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !p.IsRunning() {
		t.Error("expected poller to be running")
	}

	// Double start should fail
	if err := p.Start(ctx); err == nil {
		t.Error("expected error on second Start, got nil")
	}

	time.Sleep(80 * time.Millisecond)

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if p.IsRunning() {
		t.Error("expected poller to not be running after Stop")
	}

	// Double stop should be idempotent and return nil
	if err := p.Stop(); err != nil {
		t.Errorf("idempotent Stop returned error: %v", err)
	}
}

func TestPoller_AntiStampede_ConcurrentPollOnce(t *testing.T) {
	server := mocks.NewMockGoogleServer()
	defer server.Close()

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:          "acc-concurrent",
		Email:       "concurrent@example.com",
		AccessToken: "token-c",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusActive,
	}
	_ = accRepo.Create(ctx, acc)

	p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.PollOnce(ctx)
		}()
	}
	wg.Wait()
}

func TestPoller_DisabledAccounts_Skipped(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:          "acc-disabled",
		Email:       "disabled@example.com",
		AccessToken: "token-d",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusDisabled, // Disabled
	}
	_ = accRepo.Create(ctx, acc)

	p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}

	if called {
		t.Error("expected disabled account to be skipped, but server was called")
	}
}

func TestPoller_FetchQuotaSummary_RequestHeadersAndProjectBody(t *testing.T) {
	var capturedBody string
	var capturedUA string
	var capturedClient string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		capturedBody = string(b)
		capturedUA = r.Header.Get("User-Agent")
		capturedClient = r.Header.Get("X-Goog-Api-Client")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"groups":[]}`))
	}))
	defer server.Close()

	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:          "acc-req-check",
		Email:       "user@example.com",
		AccessToken: "token-check",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusActive,
	}
	_ = accRepo.Create(ctx, acc)

	p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	_, _ = p.fetchQuotaSummary(ctx, acc)

	if !strings.Contains(capturedBody, `"project":"aicode-consumers"`) {
		t.Errorf("expected request body to contain project:aicode-consumers, got %q", capturedBody)
	}
	if !strings.Contains(capturedUA, "aidev_client") || !strings.Contains(capturedUA, "antigravity/cli") {
		t.Errorf("expected User-Agent with aidev_client and antigravity/cli, got %q", capturedUA)
	}
	if capturedClient == "" {
		t.Errorf("expected X-Goog-Api-Client header to be set, got empty")
	}
}

func TestPoller_401RefreshRetryDispatch(t *testing.T) {
	cases := []struct {
		name           string
		retryStatus    int
		wantStatus     domain.AccountStatus
		wantBucketsMin int
		wantErr        bool
	}{
		{"401->refresh->403 consumer", http.StatusForbidden, domain.AccountStatusActive, 1, false},
		{"401->refresh->429 exhausted", http.StatusTooManyRequests, domain.AccountStatusExhausted, 0, true},
		{"401->refresh->401 error", http.StatusUnauthorized, domain.AccountStatusError, 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.WriteHeader(tc.retryStatus)
			}))
			defer srv.Close()

			acc := &domain.Account{
				ID:           "acc-401",
				Email:        "401@example.com",
				AccessToken:  "old-tok",
				RefreshToken: "rt-tok",
				TokenExpiry:  time.Now().Add(1 * time.Hour),
				Status:       domain.AccountStatusActive,
			}
			db, err := sqlite.Open(":memory:")
			if err != nil {
				t.Fatalf("sqlite.Open: %v", err)
			}
			defer db.Close()
			accRepo := sqlite.NewAccountRepository(db)
			quotaRepo := sqlite.NewQuotaRepository(db)
			_ = accRepo.Create(context.Background(), acc)
			refresher := TokenRefresherFunc(func(ctx context.Context, rt string) (string, time.Time, error) {
				return "new-tok", time.Now().Add(1 * time.Hour), nil
			})

			p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(srv.URL), WithTokenRefresher(refresher))
			if err != nil {
				t.Fatalf("NewPoller: %v", err)
			}

			_, pollErr := p.PollAccount(context.Background(), acc.ID)
			if tc.wantErr && pollErr == nil {
				t.Errorf("expected error, got nil")
			}
			updatedAcc, _ := accRepo.GetByID(context.Background(), acc.ID)
			if updatedAcc.Status != tc.wantStatus {
				t.Errorf("got status %q, want %q", updatedAcc.Status, tc.wantStatus)
			}
			if tc.wantBucketsMin > 0 {
				buckets, _ := quotaRepo.GetByAccountID(context.Background(), acc.ID)
				if len(buckets) < tc.wantBucketsMin {
					t.Errorf("expected >= %d buckets, got %d", tc.wantBucketsMin, len(buckets))
				}
			}
		})
	}
}

func TestPoller_WithNonPositivePollIntervalDoesNotPanic(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()
	accRepo := sqlite.NewAccountRepository(db)
	quotaRepo := sqlite.NewQuotaRepository(db)

	for _, interval := range []time.Duration{0, -5 * time.Second} {
		p, err := NewPoller(accRepo, quotaRepo, WithPollInterval(interval))
		if err != nil {
			t.Fatalf("NewPoller(%v): %v", interval, err)
		}
		if err := p.Start(context.Background()); err != nil {
			t.Fatalf("Start(%v): %v", interval, err)
		}
		if err := p.Stop(); err != nil {
			t.Fatalf("Stop(%v): %v", interval, err)
		}
	}
}

func TestPoller_ConcurrentPollAccountAndPollOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"groups":{"claude":{"buckets":[{"bucketId":"claude-5h","window":"5h","remainingFraction":0.8,"remaining_amount":80}]}}}`))
	}))
	defer srv.Close()

	acc := &domain.Account{
		ID:          "acc-conc",
		Email:       "conc@example.com",
		AccessToken: "tok",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusActive,
	}
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()
	accRepo := sqlite.NewAccountRepository(db)
	quotaRepo := sqlite.NewQuotaRepository(db)
	_ = accRepo.Create(context.Background(), acc)
	p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = p.PollAccount(context.Background(), acc.ID)
			} else {
				_ = p.PollOnce(context.Background())
			}
		}(i)
	}
	wg.Wait()
}

func TestParseQuotaResponse_GroupsSnakeCaseRemainingAmount(t *testing.T) {
	p := &Poller{}
	body := strings.NewReader(`{"groups":[{"name":"claude","buckets":[{"bucket_id":"claude-5h","window":"5h","remaining_fraction":0.75,"remaining_amount":150}]}]}`)
	buckets, err := p.parseQuotaResponse("acc-1", body)
	if err != nil {
		t.Fatalf("parseQuotaResponse: %v", err)
	}
	if len(buckets) != 1 || buckets[0].RemainingAmount != 150 {
		t.Fatalf("expected 1 bucket with RemainingAmount=150, got %+v", buckets)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestPoller_LanguageServerAttributionFollowsClaudeGPTCategory(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	accRepo := sqlite.NewAccountRepository(db)
	quotaRepo := sqlite.NewQuotaRepository(db)
	ctx := context.Background()

	accA := &domain.Account{
		ID:          "acc-global-active",
		Email:       "a@example.com",
		AccessToken: "tok-a",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusActive,
		IsActive:    true,
	}
	accB := &domain.Account{
		ID:          "acc-claude-owner",
		Email:       "b@example.com",
		AccessToken: "tok-b",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusActive,
	}
	if err := accRepo.Create(ctx, accA); err != nil {
		t.Fatalf("Create accA: %v", err)
	}
	if err := accRepo.Create(ctx, accB); err != nil {
		t.Fatalf("Create accB: %v", err)
	}
	// Make global is_active = accA, but primary Claude/GPT category owner = accB.
	if err := accRepo.SetActive(ctx, accA.ID); err != nil {
		t.Fatalf("SetActive accA: %v", err)
	}
	if err := accRepo.SetActiveForCategory(ctx, domain.CategoryClaudeGPT, accB.ID); err != nil {
		t.Fatalf("SetActiveForCategory accB: %v", err)
	}

	var mu sync.Mutex
	var lsCalledFor []string
	var httpTokens []string

	httpClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			httpTokens = append(httpTokens, r.Header.Get("Authorization"))
			mu.Unlock()
			payload := `{"groups":[{"displayName":"Claude & GPT","buckets":[{"bucketId":"3p-5h","window":"5h","remainingFraction":0.66}]}]}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(payload)),
			}, nil
		}),
	}

	p, err := NewPoller(
		accRepo,
		quotaRepo,
		WithHTTPClient(httpClient),
		WithLanguageServerQueryFunc(func(_ context.Context, accountID string) ([]*domain.QuotaBucket, error) {
			mu.Lock()
			lsCalledFor = append(lsCalledFor, accountID)
			mu.Unlock()
			return []*domain.QuotaBucket{{
				AccountID:         accountID,
				BucketID:          accountID + "-3p-5h",
				DisplayName:       "Claude & GPT (5h)",
				Window:            domain.QuotaWindow("5H"),
				RemainingFraction: 0.42,
				Source:            "language_server",
				UpdatedAt:         time.Now().UTC(),
			}}, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	// Polling accA (globally is_active, but NOT CategoryClaudeGPT) must skip language_server and use direct HTTP.
	bucketsA, err := p.PollAccount(ctx, accA.ID)
	if err != nil {
		t.Fatalf("PollAccount accA: %v", err)
	}
	if len(bucketsA) != 1 || bucketsA[0].RemainingFraction != 0.66 {
		t.Fatalf("expected direct HTTP fraction 0.66 for accA, got %+v", bucketsA)
	}

	// Polling accB (CategoryClaudeGPT owner) must use language_server.
	bucketsB, err := p.PollAccount(ctx, accB.ID)
	if err != nil {
		t.Fatalf("PollAccount accB: %v", err)
	}
	if len(bucketsB) != 1 || bucketsB[0].RemainingFraction != 0.42 {
		t.Fatalf("expected language_server fraction 0.42 for accB, got %+v", bucketsB)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(lsCalledFor) != 1 || lsCalledFor[0] != accB.ID {
		t.Fatalf("expected lsQueryFunc called once for %s, got %v", accB.ID, lsCalledFor)
	}
	if len(httpTokens) != 1 || httpTokens[0] != "Bearer tok-a" {
		t.Fatalf("expected direct HTTP fetch once for tok-a, got %v", httpTokens)
	}
}
