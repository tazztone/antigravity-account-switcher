package e2e

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/test/mocks"
)

// TestTier2_Transparent429Failover validates that when the upstream Google server returns
// HTTP 429 RESOURCE_EXHAUSTED for Account A, the proxy transparently rotates to Account B
// and completes the request with HTTP 200 OK without client error.
func TestTier2_Transparent429Failover(t *testing.T) {
	env := setupE2EEnvironment(t, 0)
	ctx := context.Background()

	now := time.Now().UTC()
	// Create Account A (active)
	accA := &domain.Account{
		ID:          "acc-alpha",
		Email:       "alpha@example.com",
		AccessToken: "token-alpha",
		IsActive:    true,
		Status:      domain.AccountStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	// Create Account B (healthy standby)
	accB := &domain.Account{
		ID:          "acc-beta",
		Email:       "beta@example.com",
		AccessToken: "token-beta",
		IsActive:    false,
		Status:      domain.AccountStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := env.AccountRepo.Create(ctx, accA); err != nil {
		t.Fatalf("create accA: %v", err)
	}
	if err := env.AccountRepo.Create(ctx, accB); err != nil {
		t.Fatalf("create accB: %v", err)
	}

	// Configure mock Google server:
	// token-alpha returns 429 RESOURCE_EXHAUSTED on its next request
	env.MockGoogle.SetFailoverTrigger("token-alpha", 1)
	// token-beta returns 200 OK
	env.MockGoogle.ConfigureAccount("token-beta", &mocks.AccountBehavior{
		Email:             "beta@example.com",
		FailoverRemaining: 0,
	})

	// Client sends request to the local switcher proxy
	reqBody := `{"contents":[{"role":"user","parts":[{"text":"Explain transparent failover"}]}]}`
	req, err := http.NewRequest(http.MethodPost, env.ServerURL+"/v1internal:generateContent", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client request failed: %v", err)
	}
	defer resp.Body.Close()

	// ASSERTION 1: Transparent 200 OK received by client
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected HTTP 200 OK from transparent failover, got %d: %s", resp.StatusCode, string(body))
	}

	// ASSERTION 2: Account A marked exhausted in SQLite
	updatedA, err := env.AccountRepo.GetByID(ctx, "acc-alpha")
	if err != nil {
		t.Fatalf("get accA: %v", err)
	}
	if updatedA.Status != domain.AccountStatusExhausted {
		t.Errorf("expected Account A status to be 'exhausted', got %q", updatedA.Status)
	}

	// ASSERTION 3: Account B marked active in SQLite
	active, err := env.AccountRepo.GetActive(ctx)
	if err != nil {
		t.Fatalf("get active account: %v", err)
	}
	if active.ID != "acc-beta" {
		t.Errorf("expected Account B ('acc-beta') to be active, got %s (%s)", active.Email, active.ID)
	}

	// ASSERTION 4: Upstream received requests with appropriate Bearer tokens
	recorded := env.MockGoogle.GetRecordedRequests()
	foundAlphaAttempt := false
	foundBetaAttempt := false
	for _, r := range recorded {
		if r.AuthBearer == "token-alpha" {
			foundAlphaAttempt = true
		}
		if r.AuthBearer == "token-beta" {
			foundBetaAttempt = true
		}
	}
	if !foundAlphaAttempt {
		t.Errorf("expected upstream request with Bearer token-alpha")
	}
	if !foundBetaAttempt {
		t.Errorf("expected retried upstream request with Bearer token-beta")
	}
}

// TestTier2_SSETokenCapture validates that the proxy interceptor parses real SSE streams
// and extracts promptTokenCount, candidatesTokenCount, and cachedContentTokenCount to SQLite.
func TestTier2_SSETokenCapture(t *testing.T) {
	env := setupE2EEnvironment(t, 0)
	ctx := context.Background()

	now := time.Now().UTC()
	acc := &domain.Account{
		ID:          "acc-sse-user",
		Email:       "sse_user@example.com",
		AccessToken: "token-sse",
		IsActive:    true,
		Status:      domain.AccountStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := env.AccountRepo.Create(ctx, acc); err != nil {
		t.Fatalf("create account: %v", err)
	}

	// Configure mock Google PA with custom token metrics in SSE chunk
	env.MockGoogle.ConfigureAccount("token-sse", &mocks.AccountBehavior{
		Email: "sse_user@example.com",
		Usage: &mocks.UsageMetadata{
			PromptTokenCount:        350,
			CandidatesTokenCount:    120,
			TotalTokenCount:         470,
			CachedContentTokenCount: 50,
		},
	})

	req, err := http.NewRequest(
		http.MethodPost,
		env.ServerURL+"/v1internal:streamGenerateContent?alt=sse",
		strings.NewReader(`{"contents":[{"parts":[{"text":"Stream test"}]}]}`),
	)
	if err != nil {
		t.Fatalf("create sse req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	// Consume entire SSE stream
	scanner := bufio.NewScanner(resp.Body)
	receivedChunks := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			receivedChunks++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning SSE stream: %v", err)
	}
	if receivedChunks == 0 {
		t.Fatalf("expected SSE chunks, received 0")
	}

	// Verify metrics recorded in SQLite (persisted asynchronously in StreamAndInterceptSSE)
	var summary *domain.AggregatedMetrics
	for i := 0; i < 40; i++ {
		summary, err = env.MetricsService.GetSummary(ctx, "acc-sse-user", domain.PeriodLifetime)
		if err == nil && summary != nil && summary.TotalRequests > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("get metrics summary: %v", err)
	}

	if summary.TotalPromptTokens != 350 {
		t.Errorf("expected 350 prompt tokens, got %d", summary.TotalPromptTokens)
	}
	if summary.TotalCandidatesTokens != 120 {
		t.Errorf("expected 120 candidate tokens, got %d", summary.TotalCandidatesTokens)
	}
	if summary.TotalTokens != 470 {
		t.Errorf("expected 470 total tokens, got %d", summary.TotalTokens)
	}
	if summary.TotalCachedContentTokens != 50 {
		t.Errorf("expected 50 cached content tokens, got %d", summary.TotalCachedContentTokens)
	}
	if summary.TotalRequests != 1 {
		t.Errorf("expected 1 request recorded, got %d", summary.TotalRequests)
	}
}

// TestTier2_SoloCategory_BlocksRotationOn429 validates that when a category is in Solo mode,
// HTTP 429 does NOT rotate traffic to standby accounts and surfaces the error to the client.
func TestTier2_SoloCategory_BlocksRotationOn429(t *testing.T) {
	env := setupE2EEnvironment(t, 0)
	ctx := context.Background()
	now := time.Now().UTC()

	accA := &domain.Account{
		ID:          "acc-solo-a",
		Email:       "solo_a@example.com",
		AccessToken: "token-solo-a",
		IsActive:    true,
		Status:      domain.AccountStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	accB := &domain.Account{
		ID:          "acc-solo-b",
		Email:       "solo_b@example.com",
		AccessToken: "token-solo-b",
		IsActive:    false,
		Status:      domain.AccountStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := env.AccountRepo.Create(ctx, accA); err != nil {
		t.Fatalf("create accA: %v", err)
	}
	if err := env.AccountRepo.Create(ctx, accB); err != nil {
		t.Fatalf("create accB: %v", err)
	}

	// Lock claude_gpt to Solo mode
	if err := env.AccountRepo.SetCategoryRoutingMode(ctx, domain.CategoryClaudeGPT, domain.RoutingModeSolo); err != nil {
		t.Fatalf("set category routing mode solo: %v", err)
	}

	// token-solo-a returns 429
	env.MockGoogle.SetFailoverTrigger("token-solo-a", 1)

	// Send request with Claude model
	reqBody := `{"model":"claude-3-5-haiku","contents":[{"parts":[{"text":"Solo test"}]}]}`
	req, err := http.NewRequest(http.MethodPost, env.ServerURL+"/v1internal:generateContent", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client request failed: %v", err)
	}
	defer resp.Body.Close()

	// ASSERTION 1: Error returned to client (429 or 503 from exhausted pool), NOT routed to Account B
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 429/503 for solo category exhaustion, got %d: %s", resp.StatusCode, string(body))
	}

	// ASSERTION 2: Account B was NOT activated
	activeClaude, _ := env.AccountRepo.GetActiveForCategory(ctx, domain.CategoryClaudeGPT)
	if activeClaude != nil && activeClaude.ID == "acc-solo-b" {
		t.Errorf("account B was incorrectly activated during solo mode: %s", activeClaude.ID)
	}

	// ASSERTION 3: Upstream never called with Account B
	for _, r := range env.MockGoogle.GetRecordedRequests() {
		if r.AuthBearer == "token-solo-b" {
			t.Errorf("unexpected request to Account B in solo mode")
		}
	}
}

// TestTier2_SoloCategory_IntraAccountModelFallback validates that even in Solo mode,
// intra-account primary -> secondary model fallback stays on the same account.
func TestTier2_SoloCategory_IntraAccountModelFallback(t *testing.T) {
	env := setupE2EEnvironment(t, 0)
	ctx := context.Background()
	now := time.Now().UTC()

	accA := &domain.Account{
		ID:          "acc-fallback-a",
		Email:       "fallback_a@example.com",
		AccessToken: "token-fallback-a",
		IsActive:    true,
		Status:      domain.AccountStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	accB := &domain.Account{
		ID:          "acc-fallback-b",
		Email:       "fallback_b@example.com",
		AccessToken: "token-fallback-b",
		IsActive:    false,
		Status:      domain.AccountStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := env.AccountRepo.Create(ctx, accA); err != nil {
		t.Fatalf("create accA: %v", err)
	}
	if err := env.AccountRepo.Create(ctx, accB); err != nil {
		t.Fatalf("create accB: %v", err)
	}

	// Lock gemini category to Solo mode
	if err := env.AccountRepo.SetCategoryRoutingMode(ctx, domain.CategoryGemini, domain.RoutingModeSolo); err != nil {
		t.Fatalf("set category routing mode solo: %v", err)
	}

	// Configure mock Google server:
	// token-fallback-a returns 429 on primary model (gemini-2.5-pro) on first call,
	// then succeeds when rewritten to secondary model (claude-3-5-sonnet)
	env.MockGoogle.SetFailoverTrigger("token-fallback-a", 1)

	reqBody := `{"model":"gemini-2.5-pro","contents":[{"parts":[{"text":"Fallback test"}]}]}`
	req, err := http.NewRequest(http.MethodPost, env.ServerURL+"/v1internal:generateContent", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client request failed: %v", err)
	}
	defer resp.Body.Close()

	// ASSERTION 1: Client gets 200 OK because request was rewritten to secondary on same account
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK via model fallback, got %d: %s", resp.StatusCode, string(body))
	}

	// ASSERTION 2: Account A was used throughout, Account B was NOT touched
	for _, r := range env.MockGoogle.GetRecordedRequests() {
		if r.AuthBearer == "token-fallback-b" {
			t.Errorf("unexpected request to Account B during intra-account fallback")
		}
	}
}

// TestTier2_CategoryExclusion_SkipsExcludedAccount validates that an account with
// disabled_categories for a model category is skipped during category failover.
func TestTier2_CategoryExclusion_SkipsExcludedAccount(t *testing.T) {
	env := setupE2EEnvironment(t, 0)
	ctx := context.Background()
	now := time.Now().UTC()

	accA := &domain.Account{
		ID:          "acc-excl-a",
		Email:       "excl_a@example.com",
		AccessToken: "token-excl-a",
		IsActive:    true,
		Status:      domain.AccountStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	accB := &domain.Account{
		ID:                 "acc-excl-b",
		Email:              "excl_b@example.com",
		AccessToken:        "token-excl-b",
		IsActive:           false,
		Status:             domain.AccountStatusActive,
		DisabledCategories: []string{"claude_gpt"}, // Account B excludes Claude/GPT
		CreatedAt:          now,
		UpdatedAt:          now.Add(1 * time.Second),
	}
	accC := &domain.Account{
		ID:          "acc-excl-c",
		Email:       "excl_c@example.com",
		AccessToken: "token-excl-c",
		IsActive:    false,
		Status:      domain.AccountStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now.Add(2 * time.Second),
	}
	if err := env.AccountRepo.Create(ctx, accA); err != nil {
		t.Fatalf("create accA: %v", err)
	}
	if err := env.AccountRepo.Create(ctx, accB); err != nil {
		t.Fatalf("create accB: %v", err)
	}
	if err := env.AccountRepo.Create(ctx, accC); err != nil {
		t.Fatalf("create accC: %v", err)
	}

	// Pool mode for claude_gpt
	if err := env.AccountRepo.SetCategoryRoutingMode(ctx, domain.CategoryClaudeGPT, domain.RoutingModePool); err != nil {
		t.Fatalf("set category routing mode pool: %v", err)
	}

	// token-excl-a returns 429 on next request
	env.MockGoogle.SetFailoverTrigger("token-excl-a", 1)

	// Send request for Claude model
	reqBody := `{"model":"claude-3-5-haiku","contents":[{"parts":[{"text":"Category exclusion test"}]}]}`
	req, err := http.NewRequest(http.MethodPost, env.ServerURL+"/v1internal:generateContent", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client request failed: %v", err)
	}
	defer resp.Body.Close()

	// ASSERTION 1: Transparent 200 OK
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK from failover to eligible account, got %d: %s", resp.StatusCode, string(body))
	}

	// ASSERTION 2: Failover rotated directly to Account C, completely bypassing Account B
	var foundB, foundC bool
	for _, r := range env.MockGoogle.GetRecordedRequests() {
		if r.AuthBearer == "token-excl-b" {
			foundB = true
		}
		if r.AuthBearer == "token-excl-c" {
			foundC = true
		}
	}
	if foundB {
		t.Errorf("Account B was contacted despite having claude_gpt excluded")
	}
	if !foundC {
		t.Errorf("Account C was not contacted for failover")
	}

	// ASSERTION 3: Active account for claude_gpt is now Account C
	activeClaude, err := env.AccountRepo.GetActiveForCategory(ctx, domain.CategoryClaudeGPT)
	if err != nil {
		t.Fatalf("get active claude account: %v", err)
	}
	if activeClaude.ID != "acc-excl-c" {
		t.Errorf("expected active account for claude_gpt to be acc-excl-c, got %s", activeClaude.ID)
	}
}
