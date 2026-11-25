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

	// Token metrics (<=200k per request so base-tier pricing applies cleanly):
	// Claude Opus 5.5: $5/1M in, $25/1M out. 4 x 200,000 = 800,000 uncached input tokens = $4.00 per burn!
	// Total Claude burn: acc-a delta 0.20 ($8.00 => $40/wk), acc-b delta 0.10 ($6.00 => $60/wk).
	// Delta-weighted pool MLE = $14.00 / 0.30 = $46.6667/wk (seat spread $40-$60/wk).
	var testMetrics []*domain.TokenMetric
	for i := 0; i < 4; i++ {
		testMetrics = append(testMetrics,
			&domain.TokenMetric{AccountID: "acc-a", Model: "claude-opus-5-5-high", PromptTokens: 200_000, TotalTokens: 200_000, Timestamp: tStart.Add(10 * time.Minute)},
			&domain.TokenMetric{AccountID: "acc-a", Model: "claude-opus-5-5-high", PromptTokens: 200_000, TotalTokens: 200_000, Timestamp: tStart.Add(60 * time.Minute)},
		)
	}
	// Gemini 4 Hydrogen: $1.25/1M in, $10/1M out. 5 x 200,000 output tokens = 1,000,000 = $10.00!
	for i := 0; i < 5; i++ {
		testMetrics = append(testMetrics, &domain.TokenMetric{AccountID: "acc-a", Model: "gemini-4-hydrogen-high", CandidatesTokens: 200_000, TotalTokens: 200_000, Timestamp: tStart.Add(15 * time.Minute)})
	}
	// Account B: 6 x 200,000 input tokens on Opus 5.5 = $6.00!
	for i := 0; i < 6; i++ {
		testMetrics = append(testMetrics, &domain.TokenMetric{AccountID: "acc-b", Model: "claude-opus-5-5-high", PromptTokens: 200_000, TotalTokens: 200_000, Timestamp: tStart.Add(10 * time.Minute)})
	}
	// Unpriced model request
	testMetrics = append(testMetrics, &domain.TokenMetric{AccountID: "acc-b", Model: "experimental-unpriced-v9", PromptTokens: 1000, TotalTokens: 1000, Timestamp: tStart.Add(30 * time.Minute)})

	for _, m := range testMetrics {
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
	// Delta-weighted MLE = ($8 + $6) / (0.20 + 0.10) = 46.666667
	wantClaudeMLE := 14.0 / 0.30
	if report.PoolSummary.AvgClaudeWeeklyCapUSD == nil || math.Abs(*report.PoolSummary.AvgClaudeWeeklyCapUSD-wantClaudeMLE) > 1e-4 {
		t.Fatalf("AvgClaudeWeeklyCapUSD = %v, want %v", report.PoolSummary.AvgClaudeWeeklyCapUSD, wantClaudeMLE)
	}
	if report.PoolSummary.MinClaudeWeeklyCapUSD == nil || math.Abs(*report.PoolSummary.MinClaudeWeeklyCapUSD-40.0) > 1e-4 {
		t.Fatalf("MinClaudeWeeklyCapUSD = %v, want 40.0", report.PoolSummary.MinClaudeWeeklyCapUSD)
	}
	if report.PoolSummary.MaxClaudeWeeklyCapUSD == nil || math.Abs(*report.PoolSummary.MaxClaudeWeeklyCapUSD-60.0) > 1e-4 {
		t.Fatalf("MaxClaudeWeeklyCapUSD = %v, want 60.0", report.PoolSummary.MaxClaudeWeeklyCapUSD)
	}
	if report.PoolSummary.AvgGeminiWeeklyCapUSD == nil || math.Abs(*report.PoolSummary.AvgGeminiWeeklyCapUSD-50.0) > 1e-4 {
		t.Fatalf("AvgGeminiWeeklyCapUSD = %v, want 50.0", report.PoolSummary.AvgGeminiWeeklyCapUSD)
	}
	wantCombined := wantClaudeMLE + 50.0
	if report.PoolSummary.AvgCombinedWeeklyCapUSD == nil || math.Abs(*report.PoolSummary.AvgCombinedWeeklyCapUSD-wantCombined) > 1e-4 {
		t.Fatalf("AvgCombinedWeeklyCapUSD = %v, want %v", report.PoolSummary.AvgCombinedWeeklyCapUSD, wantCombined)
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

func TestPricing_Gemini90PctCacheAndLongContextTiers(t *testing.T) {
	catalog := metrics.DefaultModelPricing()

	// 1. 90% cache discount defaults on Gemini Pro, Flash, and Flash-Lite
	pPro, ok := metrics.ResolveModelPricing("gemini-4-hydrogen", catalog)
	if !ok || pPro.CachedInputPerMillion != 0.125 {
		t.Fatalf("expected gemini-4-hydrogen cached $0.125, got %+v", pPro)
	}
	pFlash, ok := metrics.ResolveModelPricing("gemini-3.8-flash", catalog)
	if !ok || pFlash.CachedInputPerMillion != 0.03 {
		t.Fatalf("expected gemini-3.8-flash cached $0.03, got %+v", pFlash)
	}
	pLite, ok := metrics.ResolveModelPricing("gemini-3.5-flash-lite", catalog)
	if !ok || pLite.CachedInputPerMillion != 0.01 {
		t.Fatalf("expected gemini-3.5-flash-lite cached $0.01, got %+v", pLite)
	}

	// 2. Gemini Pro request > 200k prompt tokens applies 2.0x input/cached and 1.5x output
	mLongGemini := &domain.TokenMetric{
		Model:               "gemini-4-hydrogen",
		PromptTokens:        250_000,
		CachedContentTokens: 200_000, // 50k uncached ($2.50/1M), 200k cached ($0.25/1M)
		CandidatesTokens:    100_000, // 100k output ($15.00/1M)
		TotalTokens:         350_000,
	}
	cGemini := metrics.ComputeMetricCost(mLongGemini, pPro, true)
	wantGeminiUSD := (50_000*1.25*2.0 + 200_000*0.125*2.0 + 100_000*10.0*1.5) / 1e6
	if math.Abs(cGemini.APIValueUSD-wantGeminiUSD) > 1e-12 {
		t.Fatalf("Gemini >200k USD = %v, want %v", cGemini.APIValueUSD, wantGeminiUSD)
	}

	// 3. Gemini Flash and Claude Opus stay flat above 200k
	mLongFlash := &domain.TokenMetric{
		Model:            "gemini-3.8-flash",
		PromptTokens:     250_000,
		CandidatesTokens: 10_000,
		TotalTokens:      260_000,
	}
	cFlash := metrics.ComputeMetricCost(mLongFlash, pFlash, true)
	wantFlashUSD := (250_000*0.30 + 10_000*2.50) / 1e6
	if math.Abs(cFlash.APIValueUSD-wantFlashUSD) > 1e-12 {
		t.Fatalf("Flash >200k should remain flat: got %v, want %v", cFlash.APIValueUSD, wantFlashUSD)
	}

	pOpus, ok := metrics.ResolveModelPricing("claude-opus-5-5", catalog)
	if !ok {
		t.Fatalf("expected claude-opus-5-5 pricing")
	}
	mLongClaude := &domain.TokenMetric{
		Model:            "claude-opus-5-5",
		PromptTokens:     250_000,
		CandidatesTokens: 10_000,
		TotalTokens:      260_000,
	}
	cClaude := metrics.ComputeMetricCost(mLongClaude, pOpus, true)
	wantClaudeUSD := (250_000*5.0 + 10_000*25.0) / 1e6
	if math.Abs(cClaude.APIValueUSD-wantClaudeUSD) > 1e-12 {
		t.Fatalf("Claude >200k should remain flat: got %v, want %v", cClaude.APIValueUSD, wantClaudeUSD)
	}
}

func TestValueService_PreWindowAnchorAndCrossSourceContinuity(t *testing.T) {
	svc, accRepo, quotaRepo, metricsRepo, _ := setupValueTestService(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:             "acc-anchor",
		Email:          "anchor@gmail.com",
		RefreshToken:   "rt-anchor",
		PlanTier:       "ai_pro",
		MonthlyCostUSD: 20.0,
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("Create: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(4 * time.Hour)

	// Anchor snapshot at now-26h (before PeriodDay 24h cutoff) from language_server (5H, prefixed bucketID)
	// Followed by in-window snapshot at now-22h from cloud_code (5h, raw bucketID)
	_, err := quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{
		{
			AccountID:         acc.ID,
			BucketID:          acc.ID + "-3p-5h",
			DisplayName:       "Claude & GPT (5h)",
			Window:            "5H",
			RemainingFraction: 1.00,
			ResetTime:         reset,
			Source:            "language_server",
			CapturedAt:        now.Add(-26 * time.Hour),
		},
		{
			AccountID:         acc.ID,
			BucketID:          "3p-5h",
			DisplayName:       "Claude and GPT models (5h)",
			Window:            "5h",
			RemainingFraction: 0.90,
			ResetTime:         reset,
			Source:            "cloud_code",
			CapturedAt:        now.Add(-22 * time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("RecordSnapshotsIfChanged: %v", err)
	}

	// Two requests inside the (now-26h, now-22h] interval: one before 24h cutoff (-25h, $2) and one inside (-23h, $2).
	// Using claude-sonnet ($3/1M input): 200k * 3 + ... or outputs ($15/1M): 133,333.33... -> use claude-opus-4-1 ($15/1M in):
	// Actually claude-opus-5-5 is $5/1M in, $25/1M out: 80,000 output tokens = $2.00!
	for _, ts := range []time.Time{now.Add(-25 * time.Hour), now.Add(-23 * time.Hour)} {
		if err := metricsRepo.Record(ctx, &domain.TokenMetric{
			AccountID:        acc.ID,
			Model:            "claude-opus-5-5",
			CandidatesTokens: 80_000,
			TotalTokens:      80_000,
			Timestamp:        ts,
		}); err != nil {
			t.Fatalf("metricsRepo.Record: %v", err)
		}
	}

	report, err := svc.GetPlanValueReport(ctx, domain.PeriodDay, "")
	if err != nil {
		t.Fatalf("GetPlanValueReport: %v", err)
	}

	// RealizedAPIUSD for PeriodDay must count ONLY the -23h request ($2.00), NOT the -25h request.
	if math.Abs(report.PoolSummary.RealizedAPIUSD-2.0) > 1e-6 {
		t.Fatalf("RealizedAPIUSD = %v, want 2.0", report.PoolSummary.RealizedAPIUSD)
	}
	// Calibration across the anchor interval (-26h -> -22h, delta 0.10) must include BOTH $2 requests ($4 total => $40 cap).
	if report.PoolSummary.AvgClaude5hCapUSD == nil || math.Abs(*report.PoolSummary.AvgClaude5hCapUSD-40.0) > 1e-4 {
		t.Fatalf("AvgClaude5hCapUSD = %v, want 40.0", report.PoolSummary.AvgClaude5hCapUSD)
	}
	if len(report.Episodes) != 1 || report.Episodes[0].SourceStart != "language_server" || report.Episodes[0].SourceEnd != "cloud_code" {
		t.Fatalf("expected 1 cross-source episode, got %+v", report.Episodes)
	}

	// Verify Wokey.ai benchmark defaults and partial-pool combined note (Claude calibrated, Gemini uncalibrated).
	benchByID := make(map[string]domain.PlanBenchmark, len(report.Benchmarks))
	for _, b := range report.Benchmarks {
		benchByID[b.ID] = b
	}
	if b := benchByID["claude-pro"]; b.WeeklyAPIUSD != 163.0 || b.Window5hAPIUSD != 13.0 {
		t.Fatalf("unexpected claude-pro Wokey defaults: %+v", b)
	}
	if b := benchByID["codex-plus"]; b.WeeklyAPIUSD != 44.0 || b.Window5hAPIUSD != 7.10 {
		t.Fatalf("unexpected codex-plus Wokey defaults: %+v", b)
	}
	// Trigger partial-pool note by adding a Claude weekly snapshot+metric check? Even better, test appendMeasuredBenchmarks logic via a weekly burn on Claude only.
	resetWk := now.Add(5 * 24 * time.Hour)
	_, err = quotaRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{
		{AccountID: acc.ID, BucketID: acc.ID + "-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 1.00, ResetTime: resetWk, Source: "cloud_code", CapturedAt: now.Add(-2 * time.Hour)},
		{AccountID: acc.ID, BucketID: acc.ID + "-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 0.90, ResetTime: resetWk, Source: "cloud_code", CapturedAt: now.Add(-1 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("RecordSnapshotsIfChanged weekly: %v", err)
	}
	if err := metricsRepo.Record(ctx, &domain.TokenMetric{
		AccountID:        acc.ID,
		Model:            "claude-opus-5-5",
		CandidatesTokens: 80_000,
		TotalTokens:      80_000,
		Timestamp:        now.Add(-90 * time.Minute),
	}); err != nil {
		t.Fatalf("metricsRepo.Record weekly: %v", err)
	}
	partialReport, err := svc.GetPlanValueReport(ctx, domain.PeriodWeek, "")
	if err != nil {
		t.Fatalf("GetPlanValueReport partial: %v", err)
	}
	for _, b := range partialReport.Benchmarks {
		if b.ID == "agy-ai-pro-combined" && b.Notes != "Partial lower bound: Claude/GPT pool only (Gemini needs >=1% delta)" {
			t.Fatalf("expected partial lower bound note, got %q", b.Notes)
		}
	}
}
