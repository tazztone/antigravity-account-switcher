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
	if ver < 10 {
		t.Fatalf("expected schema version >= 10, got %d", ver)
	}
}

func TestQuotaSnapshots_CrossSourceUnificationAndAnchors(t *testing.T) {
	_, accRepo, quotaRepo, _, _ := setupTestStore(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:           "acc-1",
		Email:        "unify@gmail.com",
		RefreshToken: "rt-unify",
		PlanTier:     "ai_pro",
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("Create account: %v", err)
	}

	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	reset1 := t0.Add(5 * time.Hour)

	// 1. Initial language_server poll (bucketID "<accID>-<rawID>", window "5H")
	inserted, err := quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{{
		AccountID:         acc.ID,
		BucketID:          "acc-1-3p-5h",
		DisplayName:       "Claude & GPT (5h)",
		Window:            "5H",
		RemainingFraction: 1.0,
		ResetTime:         reset1,
		Source:            "language_server",
		CapturedAt:        t0,
	}})
	if err != nil || inserted != 1 {
		t.Fatalf("expected 1 baseline language_server insert, got %d (err=%v)", inserted, err)
	}

	// 2. Subsequent unchanged cloud_code poll (raw bucketID "3p-5h", window "5h") must dedup cleanly
	inserted, err = quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{{
		AccountID:         acc.ID,
		BucketID:          "3p-5h",
		DisplayName:       "Claude and GPT models (5h)",
		Window:            "5h",
		RemainingFraction: 1.0,
		ResetTime:         reset1,
		Source:            "cloud_code",
		CapturedAt:        t0.Add(5 * time.Minute),
	}})
	if err != nil || inserted != 0 {
		t.Fatalf("expected 0 inserted for unchanged cloud_code poll across sources, got %d (err=%v)", inserted, err)
	}

	// 3. Drop recorded from cloud_code must insert once with canonical window/category
	inserted, err = quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{{
		AccountID:         acc.ID,
		BucketID:          "3p-5h",
		DisplayName:       "Claude and GPT models (5h)",
		Window:            "5h",
		RemainingFraction: 0.80,
		ResetTime:         reset1,
		Source:            "cloud_code",
		CapturedAt:        t0.Add(10 * time.Minute),
	}})
	if err != nil || inserted != 1 {
		t.Fatalf("expected 1 inserted on cloud_code burn, got %d (err=%v)", inserted, err)
	}

	// Synthetic snapshot prior to anchor cutoff must be ignored by ListLatestSnapshotsBefore
	_, _ = quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{{
		AccountID:         acc.ID,
		BucketID:          "acc-1-gemini-weekly",
		DisplayName:       "Gemini (weekly)",
		Window:            "WEEKLY",
		RemainingFraction: 0.55,
		ResetTime:         reset1.Add(7 * 24 * time.Hour),
		Source:            "synthetic",
		CapturedAt:        t0.Add(2 * time.Minute),
	}})

	allSnaps, err := quotaRepo.ListSnapshots(ctx, acc.ID, time.Time{})
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if allSnaps[0].Window != "5h" || allSnaps[0].Category != "claude_gpt" {
		t.Fatalf("expected normalized 5h/claude_gpt on first snapshot, got %s/%s", allSnaps[0].Window, allSnaps[0].Category)
	}
	if allSnaps[1].Window != "5h" || allSnaps[1].Category != "claude_gpt" {
		t.Fatalf("expected normalized 5h/claude_gpt on second snapshot, got %s/%s", allSnaps[1].Window, allSnaps[1].Category)
	}

	anchors, err := quotaRepo.ListLatestSnapshotsBefore(ctx, acc.ID, t0.Add(8*time.Minute))
	if err != nil {
		t.Fatalf("ListLatestSnapshotsBefore: %v", err)
	}
	if len(anchors) != 1 {
		t.Fatalf("expected 1 non-synthetic anchor prior to t0+8m, got %d (%+v)", len(anchors), anchors)
	}
	if anchors[0].RemainingFraction != 1.0 || anchors[0].Source != "language_server" {
		t.Fatalf("unexpected anchor: %+v", anchors[0])
	}
}
