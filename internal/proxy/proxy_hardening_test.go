package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/store/sqlite"
)

func TestProxyHandler_Preserves400And404Body(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
		wantBody := `{"error":{"code":` + string(rune('0'+status/100)) + `00,"message":"detailed upstream error"}}`
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(wantBody))
		}))
		defer upstream.Close()

		db, err := sqlite.Open(":memory:")
		if err != nil {
			t.Fatalf("sqlite.Open: %v", err)
		}
		defer db.Close()

		accRepo := sqlite.NewAccountRepository(db)
		_ = accRepo.Create(context.Background(), &domain.Account{
			ID:          "acc-err-body",
			Email:       "err@example.com",
			AccessToken: "tok",
			TokenExpiry: time.Now().Add(1 * time.Hour),
			Status:      domain.AccountStatusActive,
			IsActive:    true,
		})

		h, err := NewProxyHandler(accRepo, WithTargetURL(upstream.URL))
		if err != nil {
			t.Fatalf("NewProxyHandler: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/v1internal:generateContent", strings.NewReader(`{"model":"gemini-2.5-pro"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != status {
			t.Errorf("status = %d, want %d", rec.Code, status)
		}
		if rec.Body.String() != wantBody {
			t.Errorf("body = %q, want %q", rec.Body.String(), wantBody)
		}
	}
}

func TestFailoverEngine_PoolFirstSingleAccountFallback(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	accRepo := sqlite.NewAccountRepository(db)
	quotaRepo := sqlite.NewQuotaRepository(db)
	ctx := context.Background()

	acc := &domain.Account{
		ID:          "acc-single",
		Email:       "single@example.com",
		AccessToken: "tok",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusActive,
		IsActive:    true,
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	fe := NewFailoverEngine(accRepo, nil, nil)
	fe.SetQuotaRepository(quotaRepo)
	fe.SetFallbackConfig("claude-opus-4-6", "claude-sonnet-4-6", true)
	fe.UpdateQuotaCache(acc.ID, []*domain.QuotaBucket{
		{AccountID: acc.ID, BucketID: "claude-sonnet", Window: "5h", RemainingFraction: 0.8},
	})

	action, targetModel, nextAcc, err := fe.HandleExhaustion(ctx, acc, "claude-opus-4-6")
	if err != nil {
		t.Fatalf("HandleExhaustion: %v", err)
	}
	if action != ActionFallbackSecondary || targetModel != "claude-sonnet-4-6" || nextAcc.ID != acc.ID {
		t.Errorf("got (%v, %q, %v), want (ActionFallbackSecondary, claude-sonnet-4-6, %s)", action, targetModel, nextAcc, acc.ID)
	}

	gotAcc, _ := accRepo.GetByID(ctx, acc.ID)
	if gotAcc.Status != domain.AccountStatusActive {
		t.Errorf("expected DB status to remain active, got %q", gotAcc.Status)
	}
}

func TestFailoverEngine_ConcurrentConfigAndRepoUpdates(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	accRepo := sqlite.NewAccountRepository(db)
	quotaRepo := sqlite.NewQuotaRepository(db)
	acc := &domain.Account{ID: "acc-race", Email: "race@example.com", Status: domain.AccountStatusActive}
	_ = accRepo.Create(context.Background(), acc)

	fe := NewFailoverEngine(accRepo, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				fe.SetFallbackConfig("claude-opus-4-6", "claude-sonnet-4-6", true)
			} else if i%3 == 1 {
				fe.SetQuotaRepository(quotaRepo)
			} else {
				_, _, _ = fe.PredictiveCheck(context.Background(), acc, "claude-opus-4-6")
			}
		}(i)
	}
	wg.Wait()
}

func TestProxyHandler_GzippedSSEResponseStripsContentEncoding(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n"))
		_ = gz.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	}))
	defer upstream.Close()

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	accRepo := sqlite.NewAccountRepository(db)
	_ = accRepo.Create(context.Background(), &domain.Account{
		ID:          "acc-gz",
		Email:       "gz@example.com",
		AccessToken: "tok",
		TokenExpiry: time.Now().Add(1 * time.Hour),
		Status:      domain.AccountStatusActive,
		IsActive:    true,
	})

	h, err := NewProxyHandler(accRepo, WithTargetURL(upstream.URL))
	if err != nil {
		t.Fatalf("NewProxyHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1internal:streamGenerateContent?alt=sse", strings.NewReader(`{"model":"gemini-2.5-pro"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "" {
		t.Errorf("expected Content-Encoding to be stripped, got %q", ce)
	}
}

type mockBroadcaster struct {
	mu     sync.Mutex
	events []*domain.ProxyEvent
}

func (m *mockBroadcaster) Broadcast(e *domain.ProxyEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

func (m *mockBroadcaster) Subscribe() (<-chan *domain.ProxyEvent, func()) {
	ch := make(chan *domain.ProxyEvent)
	return ch, func() { close(ch) }
}

func TestStreamAndInterceptSSEWithMeta_NilMetricsRepoBroadcastsEvent(t *testing.T) {
	sseBody := "data: {\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15}}\n\n"
	rec := httptest.NewRecorder()
	bc := &mockBroadcaster{}

	err := StreamAndInterceptSSEWithMeta(context.Background(), rec, io.NopCloser(strings.NewReader(sseBody)), "acc-1", "/v1:stream", nil, nil, bc)
	if err != nil {
		t.Fatalf("StreamAndInterceptSSEWithMeta: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		bc.mu.Lock()
		n := len(bc.events)
		bc.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("expected EventTypeTokensCaptured to be broadcast even when metricsRepo is nil")
}

func TestGetPayloadAdapter_GeminiArgonModel(t *testing.T) {
	if GetPayloadAdapter(CategorizeModel("gemini-4-argon-high")) != defaultGeminiAdapter {
		t.Error("expected gemini-4-argon-high to use defaultGeminiAdapter")
	}
}

func TestProxyHandler_Persistent401RefreshesAtMostOnce(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	accRepo := sqlite.NewAccountRepository(db)
	_ = accRepo.Create(context.Background(), &domain.Account{
		ID:           "acc-401",
		Email:        "401@example.com",
		AccessToken:  "old",
		RefreshToken: "rt",
		TokenExpiry:  time.Now().Add(1 * time.Hour),
		Status:       domain.AccountStatusActive,
		IsActive:     true,
	})

	var refreshCount atomic.Int32
	refresher := &mockTokenRefresher{fn: func(ctx context.Context, rt string) (string, time.Time, error) {
		refreshCount.Add(1)
		return "new-tok", time.Now().Add(1 * time.Hour), nil
	}}

	h, err := NewProxyHandler(accRepo, WithTargetURL(upstream.URL), WithTokenRefresher(refresher))
	if err != nil {
		t.Fatalf("NewProxyHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1internal:generateContent", strings.NewReader(`{"model":"gemini-2.5-pro"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if refreshCount.Load() != 1 {
		t.Errorf("expected at most 1 refresh attempt, got %d", refreshCount.Load())
	}
}

type mockTokenRefresher struct {
	fn func(ctx context.Context, rt string) (string, time.Time, error)
}

func (m *mockTokenRefresher) RefreshToken(ctx context.Context, rt string) (string, time.Time, error) {
	return m.fn(ctx, rt)
}

func TestExtractRequestTelemetry_WhitespaceSessionIDFallsBackToContentHash(t *testing.T) {
	body := `{"request":{"session_id":"   ","contents":[{"role":"user","parts":[{"text":"hello world"}]}]}}`
	tel := ExtractRequestTelemetry(http.Header{}, []byte(body), "gemini-2.5-pro", "gemini-2.5-pro")
	if tel.ConversationSource != "content_hash" || tel.ConversationID == "" {
		t.Errorf("expected content_hash fallback, got source=%q id=%q", tel.ConversationSource, tel.ConversationID)
	}
}

func TestMergeModelListResponses_EmptyDisplayNameSortEntry(t *testing.T) {
	r1 := []byte(`{"models":{"m1":{"displayName":"M1"}},"agentModelSorts":[{"displayName":"","groups":[{"modelIds":["m1"]}]}]}`)
	r2 := []byte(`{"models":{"m2":{"displayName":"M2"}},"agentModelSorts":[{"displayName":"","groups":[{"modelIds":["m2"]}]}]}`)

	merged, err := MergeModelListResponses([][]byte{r1, r2})
	if err != nil {
		t.Fatalf("MergeModelListResponses: %v", err)
	}
	var out struct {
		AgentModelSorts []json.RawMessage `json:"agentModelSorts"`
	}
	if err := json.Unmarshal(merged, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(out.AgentModelSorts) != 1 {
		t.Errorf("expected 1 merged sort entry for empty displayName, got %d", len(out.AgentModelSorts))
	}
}
