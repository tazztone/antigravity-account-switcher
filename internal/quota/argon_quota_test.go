package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

func TestArgonQuota_ConsumerBuckets_IncludesArgonWeekly(t *testing.T) {
	_, accRepo, quotaRepo := setupTestStore(t)
	p, err := NewPoller(accRepo, quotaRepo)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	acc := &domain.Account{
		ID:     "acc-consumer-argon",
		Email:  "user@gmail.com",
		Status: domain.AccountStatusActive,
	}

	buckets := p.generateConsumerQuotaBuckets(context.Background(), acc)
	if len(buckets) != 5 {
		t.Fatalf("expected 5 buckets for consumer account, got %d", len(buckets))
	}

	var argonBucket *domain.QuotaBucket
	for _, b := range buckets {
		if strings.Contains(b.BucketID, "argon-weekly") {
			argonBucket = b
			break
		}
	}

	if argonBucket == nil {
		t.Fatalf("expected argon-weekly bucket to be present")
	}
	if argonBucket.RemainingFraction != 1.0 {
		t.Errorf("expected remaining fraction 1.0, got %f", argonBucket.RemainingFraction)
	}
	if argonBucket.Window != domain.QuotaWindowWeekly && argonBucket.Window != domain.QuotaWindow("weekly") {
		t.Errorf("expected weekly window, got %s", argonBucket.Window)
	}
}

func TestArgonQuota_ParseResponse_SynthesizesArgonWhenMissing(t *testing.T) {
	_, accRepo, quotaRepo := setupTestStore(t)
	p, err := NewPoller(accRepo, quotaRepo)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	// Simulated Cloud Code PA payload returning only Gemini and Claude groups
	rawJSON := `{
		"groups": [
			{
				"displayName": "Claude and GPT models",
				"buckets": [
					{"bucketId": "3p-5h", "window": "5H", "remainingFraction": 1.0},
					{"bucketId": "3p-weekly", "window": "WEEKLY", "remainingFraction": 0.85, "resetTime": "2026-11-01T00:00:00Z"}
				]
			},
			{
				"displayName": "Gemini Models",
				"buckets": [
					{"bucketId": "gemini-5h", "window": "5H", "remainingFraction": 1.0},
					{"bucketId": "gemini-weekly", "window": "WEEKLY", "remainingFraction": 0.95, "resetTime": "2026-11-01T00:00:00Z"}
				]
			}
		]
	}`

	buckets, parseErr := p.parseQuotaResponse("acc-test", strings.NewReader(rawJSON))
	if parseErr != nil {
		t.Fatalf("parseQuotaResponse failed: %v", parseErr)
	}

	var argonBucket *domain.QuotaBucket
	for _, b := range buckets {
		if strings.Contains(strings.ToLower(b.BucketID), "argon") {
			argonBucket = b
			break
		}
	}

	if argonBucket == nil {
		t.Fatalf("expected synthesized argon-weekly bucket")
	}
	if argonBucket.BucketID != "argon-weekly" {
		t.Errorf("expected bucket ID argon-weekly, got %s", argonBucket.BucketID)
	}
	if argonBucket.RemainingFraction != 1.0 {
		t.Errorf("expected remaining fraction 1.0, got %f", argonBucket.RemainingFraction)
	}
	expectedReset, _ := time.Parse(time.RFC3339, "2026-11-01T00:00:00Z")
	if !argonBucket.ResetTime.Equal(expectedReset) {
		t.Errorf("expected reset time %v, got %v", expectedReset, argonBucket.ResetTime)
	}
}

func TestArgonQuota_ParseResponse_ArgonGroupPrefixRewrite(t *testing.T) {
	_, accRepo, quotaRepo := setupTestStore(t)
	p, err := NewPoller(accRepo, quotaRepo)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	// Payload where upstream returns an Argon group with generic "weekly" bucketId
	rawJSON := `{
		"groups": [
			{
				"displayName": "Gemini 4 Argon",
				"buckets": [
					{"bucketId": "weekly", "window": "WEEKLY", "remainingFraction": 0.70, "resetTime": "2026-11-02T12:00:00Z"}
				]
			}
		]
	}`

	buckets, parseErr := p.parseQuotaResponse("acc-test", strings.NewReader(rawJSON))
	if parseErr != nil {
		t.Fatalf("parseQuotaResponse failed: %v", parseErr)
	}

	if len(buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(buckets))
	}
	if buckets[0].BucketID != "argon-weekly" {
		t.Errorf("expected bucket ID to be rewritten to argon-weekly, got %s", buckets[0].BucketID)
	}
	if buckets[0].RemainingFraction != 0.70 {
		t.Errorf("expected remaining fraction 0.70, got %f", buckets[0].RemainingFraction)
	}
}

func TestArgonQuota_PollAccount_PreservesActiveCooldown(t *testing.T) {
	_, accRepo, quotaRepo := setupTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	cooldownExpiry := now.Add(18 * time.Hour)

	acc := &domain.Account{
		ID:          "acc-cooldown-test",
		Email:       "cooldown@example.com",
		AccessToken: "test-token",
		TokenExpiry: now.Add(1 * time.Hour),
		Status:      domain.AccountStatusActive,
	}
	_ = accRepo.Create(ctx, acc)

	// Pre-seed an active cooldown on argon-weekly in SQLite
	initialArgon := &domain.QuotaBucket{
		AccountID:         acc.ID,
		BucketID:          "argon-weekly",
		DisplayName:       "Gemini 4 Argon (weekly)",
		Window:            domain.QuotaWindow("weekly"),
		RemainingFraction: 0.0,
		ResetTime:         cooldownExpiry,
		UpdatedAt:         now,
	}
	_ = quotaRepo.UpsertBuckets(ctx, []*domain.QuotaBucket{initialArgon})

	// Upstream returns 2 groups (no Argon group)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"groups": [
				{
					"displayName": "Claude and GPT models",
					"buckets": [{"bucketId": "3p-5h", "window": "5H", "remainingFraction": 1.0}]
				},
				{
					"displayName": "Gemini Models",
					"buckets": [{"bucketId": "gemini-5h", "window": "5H", "remainingFraction": 1.0}]
				}
			]
		}`))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	p, err := NewPoller(accRepo, quotaRepo, WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	polledBuckets, pollErr := p.PollAccount(ctx, acc.ID)
	if pollErr != nil {
		t.Fatalf("PollAccount failed: %v", pollErr)
	}

	var argonPolled *domain.QuotaBucket
	for _, b := range polledBuckets {
		if strings.Contains(b.BucketID, "argon") {
			argonPolled = b
			break
		}
	}

	if argonPolled == nil {
		t.Fatalf("expected argon bucket in polled results")
	}
	// Verify that the active 0.0 fraction and cooldown resetTime were preserved
	if argonPolled.RemainingFraction != 0.0 {
		t.Errorf("expected active cooldown remaining fraction 0.0 to be preserved, got %f", argonPolled.RemainingFraction)
	}
	if !argonPolled.ResetTime.Equal(cooldownExpiry) {
		t.Errorf("expected reset time %v, got %v", cooldownExpiry, argonPolled.ResetTime)
	}
}
