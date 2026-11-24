package metrics_test

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/metrics"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/store/sqlite"
)

func setupValueTestService(t *testing.T) (*metrics.Service, *sqlite.AccountRepository, *sqlite.QuotaRepository, *sqlite.MetricsRepository, *sqlite.ValueRepository) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "value_test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	accRepo := sqlite.NewAccountRepository(db)
	quotaRepo := sqlite.NewQuotaRepository(db)
	metricsRepo := sqlite.NewMetricsRepository(db)
	valueRepo := sqlite.NewValueRepository(db)

	svc := metrics.NewService(metricsRepo, accRepo)
	svc.SetQuotaRepository(quotaRepo)
	svc.SetValueStore(valueRepo)

	return svc, accRepo, quotaRepo, metricsRepo, valueRepo
}

func TestPricingAndTokenOverlapProtection(t *testing.T) {
	catalog := metrics.DefaultModelPricing()

	p, ok := metrics.ResolveModelPricing("models/claude-opus-5-5-high", catalog)
	if !ok || p.ModelPattern != "claude-opus-5-5" {
		t.Fatalf("expected claude-opus-5-5 pricing match, got %+v (ok=%v)", p, ok)
	}

	// Case 1: TotalTokens includes thoughts -> ThoughtsTokens NOT double-counted beyond CandidatesTokens
	// Prompt=1000, Cached=600 -> uncached=400 ($5/1M) + cached=600 ($0.50/1M); Candidates=200 ($25/1M); ToolUse=100 ($5/1M); CacheWrite=200 ($6.25/1M)
	// TotalTokens = 1000 + 200 (thoughts already inside CandidatesTokens)
	mOverlap := &domain.TokenMetric{
		Model:               "claude-opus-5-5-high",
		PromptTokens:        1000,
		CachedContentTokens: 600,
		CandidatesTokens:    200,
		ThoughtsTokens:      50,
		TotalTokens:         1200,
		ToolUsePromptTokens: 100,
		CacheCreationTokens: 200,
	}
	b1 := metrics.ComputeMetricCost(mOverlap, p, true)
	if b1.UncachedInputTokens != 500 || b1.CachedInputTokens != 600 || b1.BillableOutput != 200 {
		t.Fatalf("unexpected overlap breakdown: %+v", b1)
	}
	wantUSD1 := (500*5.0 + 600*0.5 + 200*6.25 + 200*25.0) / 1e6
	if math.Abs(b1.APIValueUSD-wantUSD1) > 1e-12 {
		t.Fatalf("overlap USD = %v, want %v", b1.APIValueUSD, wantUSD1)
	}

	// Case 2: TotalTokens includes separate ThoughtsTokens -> ThoughtsTokens billed as output
	mSeparate := &domain.TokenMetric{
		Model:               "claude-opus-5-5-high",
		PromptTokens:        1000,
		CachedContentTokens: 600,
		CandidatesTokens:    200,
		ThoughtsTokens:      150,
		TotalTokens:         1350,
	}
	b2 := metrics.ComputeMetricCost(mSeparate, p, true)
	if b2.BillableOutput != 350 {
		t.Fatalf("expected separate thoughts billed (350), got %d", b2.BillableOutput)
	}

	// Case 3: Unknown model returns ok=false and zero USD
	_, okUnknown := metrics.ResolveModelPricing("mystery-model-x", catalog)
	if okUnknown {
		t.Fatalf("expected unpriced false for mystery-model-x")
	}

	// Case 4: Custom override takes precedence over built-in defaults
	merged := metrics.MergePricingCatalog([]domain.ModelPricing{{
		ModelPattern:          "claude-opus-5-5",
		DisplayName:           "Custom Opus",
		InputPerMillion:       10.0,
		CachedInputPerMillion: 1.0,
		OutputPerMillion:      50.0,
	}})
	pOver, okOver := metrics.ResolveModelPricing("claude-opus-5-5-thinking", merged)
	if !okOver || !pOver.IsOverride || pOver.InputPerMillion != 10.0 {
		t.Fatalf("expected override pricing, got %+v", pOver)
	}
}

func TestValueService_MonotonicCalibrationAndPoolAggregation(t *testing.T) {
	svc, accRepo, quotaRepo, metricsRepo, _ := setupValueTestService(t)
	ctx := context.Background()

	// Create 2 AI Pro accounts to verify pool averaging & cohort variance
	for _, a := range []*domain.Account{
		{ID: "acc-a", Email: "a@gmail.com", RefreshToken: "rt-a", PlanTier: "ai_pro", MonthlyCostUSD: 20.0},
		{ID: "acc-b", Email: "b@gmail.com", RefreshToken: "rt-b", PlanTier: "ai_pro", MonthlyCostUSD: 20.0},
	} {
		if err := accRepo.Create(ctx, a); err != nil {
			t.Fatalf("Create %s: %v", a.ID, err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	tStart := now.Add(-2 * time.Hour)
	resetEpoch1 := now.Add(5 * 24 * time.Hour)
	resetEpoch2 := resetEpoch1.Add(7 * 24 * time.Hour)

	// Account A: Claude weekly drops 0.80 -> 0.70 (delta 0.10) with $4.00 API burn -> implied $40/wk
	// Then synthetic snapshot (ignored), then reset epoch jump 0.70 -> 1.00 (ignored),
	// then external drop 1.00 -> 0.90 with 0 proxy requests (ignored),
	// then valid second epoch drop 0.90 -> 0.80 (delta 0.10) with $4.00 -> total delta 0.20, $8.00 => implied $40/wk!
	_, err := quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{
		{AccountID: "acc-a", BucketID: "acc-a-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 0.80, ResetTime: resetEpoch1, Source: "cloud_code", CapturedAt: tStart},
		{AccountID: "acc-a", BucketID: "acc-a-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 0.70, ResetTime: resetEpoch1, Source: "cloud_code", CapturedAt: tStart.Add(20 * time.Minute)},
		{AccountID: "acc-a", BucketID: "acc-a-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 0.50, ResetTime: resetEpoch1, Source: "synthetic", CapturedAt: tStart.Add(30 * time.Minute)},
		{AccountID: "acc-a", BucketID: "acc-a-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 1.00, ResetTime: resetEpoch2, Source: "cloud_code", CapturedAt: tStart.Add(40 * time.Minute)},
		{AccountID: "acc-a", BucketID: "acc-a-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 0.90, ResetTime: resetEpoch2, Source: "cloud_code", CapturedAt: tStart.Add(50 * time.Minute)},
		{AccountID: "acc-a", BucketID: "acc-a-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 0.80, ResetTime: resetEpoch2, Source: "cloud_code", CapturedAt: tStart.Add(70 * time.Minute)},
		// Gemini weekly for acc-a: 1.00 -> 0.80 (delta 0.20) with $10.00 Gemini burn -> implied $50/wk
		{AccountID: "acc-a", BucketID: "acc-a-gemini-weekly", Category: "gemini", Window: "weekly", RemainingFraction: 1.00, ResetTime: resetEpoch1, Source: "cloud_code", CapturedAt: tStart},
		{AccountID: "acc-a", BucketID: "acc-a-gemini-weekly", Category: "gemini", Window: "weekly", RemainingFraction: 0.80, ResetTime: resetEpoch1, Source: "cloud_code", CapturedAt: tStart.Add(25 * time.Minute)},
		// Account B: Claude weekly drops 0.90 -> 0.80 (delta 0.10) with $6.00 API burn -> implied $60/wk (pool avg Claude weekly = ($40+$60)/2 = $50/wk)
		{AccountID: "acc-b", BucketID: "acc-b-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 0.90, ResetTime: resetEpoch1, Source: "cloud_code", CapturedAt: tStart},
		{AccountID: "acc-b", BucketID: "acc-b-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 0.80, ResetTime: resetEpoch1, Source: "cloud_code", CapturedAt: tStart.Add(20 * time.Minute)},
	})
	if err != nil {
		t.Fatalf("RecordSnapshotsIfChanged: %v", err)
	}

	// Token metrics:
	// Claude Opus 5.5: $5/1M in, $25/1M out. 800,000 uncached input tokens = $4.00!
	for _, m := range []*domain.TokenMetric{
		{AccountID: "acc-a", Model: "claude-opus-5-5-high", PromptTokens: 800_000, TotalTokens: 800_000, Timestamp: tStart.Add(10 * time.Minute)},
		{AccountID: "acc-a", Model: "claude-opus-5-5-high", PromptTokens: 800_000, TotalTokens: 800_000, Timestamp: tStart.Add(60 * time.Minute)},
		// Gemini 4 Hydrogen: $1.25/1M in, $10/1M out. 1,000,000 output tokens = $10.00!
		{AccountID: "acc-a", Model: "gemini-4-hydrogen-high", CandidatesTokens: 1_000_000, TotalTokens: 1_000_000, Timestamp: tStart.Add(15 * time.Minute)},
		// Account B: 1,200,000 input tokens on Opus 5.5 = $6.00!
		{AccountID: "acc-b", Model: "claude-opus-5-5-high", PromptTokens: 1_200_000, TotalTokens: 1_200_000, Timestamp: tStart.Add(10 * time.Minute)},
		// Unpriced model request
		{AccountID: "acc-b", Model: "experimental-unpriced-v9", PromptTokens: 1000, TotalTokens: 1000, Timestamp: tStart.Add(30 * time.Minute)},
	} {
		if err := metricsRepo.Record(ctx, m); err != nil {
			t.Fatalf("metricsRepo.Record: %v", err)
		}
	}

	report, err := svc.GetPlanValueReport(ctx, domain.PeriodWeek, "")
	if err != nil {
		t.Fatalf("GetPlanValueReport: %v", err)
	}

	if report.PoolSummary.TotalAccounts != 2 || report.PoolSummary.AIProAccounts != 2 {
		t.Fatalf("expected 2 AI Pro accounts, got %+v", report.PoolSummary)
	}
	// Total realized = $4 + $4 + $10 + $6 = $24.00
	if math.Abs(report.PoolSummary.RealizedAPIUSD-24.0) > 1e-6 {
		t.Fatalf("RealizedAPIUSD = %v, want 24.0", report.PoolSummary.RealizedAPIUSD)
	}
	if report.PoolSummary.AvgClaudeWeeklyCapUSD == nil || math.Abs(*report.PoolSummary.AvgClaudeWeeklyCapUSD-50.0) > 1e-4 {
		t.Fatalf("AvgClaudeWeeklyCapUSD = %v, want 50.0", report.PoolSummary.AvgClaudeWeeklyCapUSD)
	}
	if report.PoolSummary.AvgGeminiWeeklyCapUSD == nil || math.Abs(*report.PoolSummary.AvgGeminiWeeklyCapUSD-50.0) > 1e-4 {
		t.Fatalf("AvgGeminiWeeklyCapUSD = %v, want 50.0", report.PoolSummary.AvgGeminiWeeklyCapUSD)
	}
	if report.PoolSummary.AvgCombinedWeeklyCapUSD == nil || math.Abs(*report.PoolSummary.AvgCombinedWeeklyCapUSD-100.0) > 1e-4 {
		t.Fatalf("AvgCombinedWeeklyCapUSD = %v, want 100.0", report.PoolSummary.AvgCombinedWeeklyCapUSD)
	}
	if report.PoolSummary.ExternalDropIntervalCount != 1 {
		t.Fatalf("expected 1 external drop interval ignored, got %d", report.PoolSummary.ExternalDropIntervalCount)
	}
	if len(report.PoolSummary.UnpricedModels) != 1 || report.PoolSummary.UnpricedModels[0] != "experimental-unpriced-v9" {
		t.Fatalf("expected unpriced [experimental-unpriced-v9], got %v", report.PoolSummary.UnpricedModels)
	}
	if len(report.Episodes) != 4 {
		t.Fatalf("expected 4 valid calibration episodes, got %d", len(report.Episodes))
	}
	if len(report.Benchmarks) < 8 {
		t.Fatalf("expected 3 measured AGY rows + 5 competitor rows, got %d", len(report.Benchmarks))
	}
}
