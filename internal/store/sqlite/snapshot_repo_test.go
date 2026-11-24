package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/store/sqlite"
)

func TestQuotaSnapshots_DeduplicationAndResetEdges(t *testing.T) {
	_, accRepo, quotaRepo, _, _ := setupTestStore(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:           "acc-snap",
		Email:        "snap@gmail.com",
		RefreshToken: "rt-snap",
		PlanTier:     "ai_pro",
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("Create account: %v", err)
	}
	if acc.MonthlyCostUSD != 20.0 {
		t.Fatalf("expected default ai_pro MonthlyCostUSD 20.0, got %v", acc.MonthlyCostUSD)
	}

	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	reset1 := t0.Add(5 * time.Hour)

	// 1. Initial baseline insert
	inserted, err := quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{{
		AccountID:         acc.ID,
		BucketID:          "acc-snap-3p-5h",
		DisplayName:       "Claude & GPT (5h)",
		Window:            "5h",
		RemainingFraction: 1.0,
		ResetTime:         reset1,
		Source:            "cloud_code",
		CapturedAt:        t0,
	}})
	if err != nil {
		t.Fatalf("RecordSnapshotsIfChanged baseline: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("expected 1 inserted baseline snapshot, got %d", inserted)
	}

	// 2. Identical poll (tiny 30s reset jitter) must be skipped
	inserted, err = quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{{
		AccountID:         acc.ID,
		BucketID:          "acc-snap-3p-5h",
		DisplayName:       "Claude & GPT (5h)",
		Window:            "5h",
		RemainingFraction: 1.0,
		ResetTime:         reset1.Add(30 * time.Second),
		Source:            "cloud_code",
		CapturedAt:        t0.Add(5 * time.Minute),
	}})
	if err != nil {
		t.Fatalf("RecordSnapshotsIfChanged duplicate: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("expected 0 inserted for unchanged poll, got %d", inserted)
	}

	// 3. Burn drop should be inserted
	inserted, err = quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{{
		AccountID:         acc.ID,
		BucketID:          "acc-snap-3p-5h",
		DisplayName:       "Claude & GPT (5h)",
		Window:            "5h",
		RemainingFraction: 0.72,
		ResetTime:         reset1,
		Source:            "cloud_code",
		CapturedAt:        t0.Add(10 * time.Minute),
	}})
	if err != nil || inserted != 1 {
		t.Fatalf("expected 1 inserted on burn drop, got %d (err=%v)", inserted, err)
	}

	// 4. Same fraction (0.0) but 429_exhaustion edge should insert, then reset shift (>2m) should insert
	inserted, err = quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{{
		AccountID:         acc.ID,
		BucketID:          "acc-snap-3p-5h",
		DisplayName:       "Claude & GPT (5h)",
		Window:            "5h",
		RemainingFraction: 0.0,
		ResetTime:         reset1,
		Source:            "429_exhaustion",
		CapturedAt:        t0.Add(20 * time.Minute),
	}})
	if err != nil || inserted != 1 {
		t.Fatalf("expected 1 inserted on 429 edge, got %d (err=%v)", inserted, err)
	}

	inserted, err = quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{{
		AccountID:         acc.ID,
		BucketID:          "acc-snap-3p-5h",
		DisplayName:       "Claude & GPT (5h)",
		Window:            "5h",
		RemainingFraction: 0.0,
		ResetTime:         reset1.Add(5 * time.Hour),
		Source:            "cloud_code",
		CapturedAt:        t0.Add(25 * time.Minute),
	}})
	if err != nil || inserted != 1 {
		t.Fatalf("expected 1 inserted on source/reset change, got %d (err=%v)", inserted, err)
	}

	snaps, err := quotaRepo.ListSnapshots(ctx, acc.ID, time.Time{})
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 4 {
		t.Fatalf("expected 4 recorded snapshots, got %d", len(snaps))
	}
	if snaps[0].Category != "claude_gpt" {
		t.Errorf("expected auto-categorized claude_gpt, got %q", snaps[0].Category)
	}
}

func TestAccountPlanAndExtendedMetrics(t *testing.T) {
	_, accRepo, _, metricsRepo, _ := setupTestStore(t)
	ctx := context.Background()

	freeAcc := &domain.Account{
		ID:             "acc-free",
		Email:          "free@gmail.com",
		RefreshToken:   "rt-free",
		PlanTier:       "free",
		MonthlyCostUSD: 0.0,
	}
	if err := accRepo.Create(ctx, freeAcc); err != nil {
		t.Fatalf("Create free account: %v", err)
	}
	got, err := accRepo.GetByID(ctx, "acc-free")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.PlanTier != "free" || got.MonthlyCostUSD != 0.0 {
		t.Fatalf("expected free/$0.0, got %s/$%v", got.PlanTier, got.MonthlyCostUSD)
	}

	if err := accRepo.UpdatePlan(ctx, "acc-free", "ai_ultra", 249.99); err != nil {
		t.Fatalf("UpdatePlan: %v", err)
	}
	got, _ = accRepo.GetByID(ctx, "acc-free")
	if got.PlanTier != "ai_ultra" || got.MonthlyCostUSD != 249.99 {
		t.Fatalf("expected ai_ultra/$249.99 after UpdatePlan, got %s/$%v", got.PlanTier, got.MonthlyCostUSD)
	}

	ts := time.Now().UTC().Truncate(time.Second)
	m := &domain.TokenMetric{
		AccountID:           "acc-free",
		RequestPath:         "/v1internal:streamGenerateContent",
		Model:               "claude-opus-5-5-high",
		PromptTokens:        1000,
		CandidatesTokens:    200,
		TotalTokens:         1500,
		CachedContentTokens: 600,
		ThoughtsTokens:      300,
		ToolUsePromptTokens: 120,
		CacheCreationTokens: 80,
		Timestamp:           ts,
	}
	if err := metricsRepo.Record(ctx, m); err != nil {
		t.Fatalf("Record metric: %v", err)
	}

	list, err := metricsRepo.ListMetricsSince(ctx, "acc-free", ts.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ListMetricsSince: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(list))
	}
	if list[0].ToolUsePromptTokens != 120 || list[0].CacheCreationTokens != 80 {
		t.Fatalf("expected tool_use=120 cache_create=80, got tool_use=%d cache_create=%d",
			list[0].ToolUsePromptTokens, list[0].CacheCreationTokens)
	}

	// Verify DB schema v9 applied cleanly on file-backed DB
	dbPath := filepath.Join(t.TempDir(), "v9.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()
	var ver int
	if err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&ver); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if ver < 9 {
		t.Fatalf("expected schema version >= 9, got %d", ver)
	}
}
