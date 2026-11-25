package metrics

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

var _ domain.ValueAnalyticsService = (*Service)(nil)

// SetQuotaRepository attaches a QuotaRepository for live bucket and snapshot calibration queries.
func (s *Service) SetQuotaRepository(repo domain.QuotaRepository) {
	s.quotaRepo = repo
}

// SetValueStore attaches a ValueAnalyticsRepository for pricing overrides and competitor benchmarks.
func (s *Service) SetValueStore(repo domain.ValueAnalyticsRepository) {
	s.valueStore = repo
}

// ListPricingCatalog returns the merged pricing catalog (overrides + built-in defaults).
func (s *Service) ListPricingCatalog(ctx context.Context) ([]domain.ModelPricing, error) {
	var overrides []domain.ModelPricing
	if s.valueStore != nil {
		var err error
		overrides, err = s.valueStore.ListPricingOverrides(ctx)
		if err != nil {
			return nil, err
		}
	}
	return MergePricingCatalog(overrides), nil
}

// UpsertPricingOverride persists a custom model pricing override.
func (s *Service) UpsertPricingOverride(ctx context.Context, p domain.ModelPricing) error {
	if s.valueStore == nil {
		return fmt.Errorf("value analytics store not configured")
	}
	return s.valueStore.UpsertPricingOverride(ctx, p)
}

// DeletePricingOverride removes a custom model pricing override.
func (s *Service) DeletePricingOverride(ctx context.Context, pattern string) error {
	if s.valueStore == nil {
		return fmt.Errorf("value analytics store not configured")
	}
	return s.valueStore.DeletePricingOverride(ctx, pattern)
}

// ListPlanBenchmarks returns merged competitor plan benchmarks.
func (s *Service) ListPlanBenchmarks(ctx context.Context) ([]domain.PlanBenchmark, error) {
	var custom []domain.PlanBenchmark
	if s.valueStore != nil {
		var err error
		custom, err = s.valueStore.ListPlanBenchmarks(ctx)
		if err != nil {
			return nil, err
		}
	}
	return MergePlanBenchmarks(custom), nil
}

// UpsertPlanBenchmark persists a competitor plan benchmark override or custom row.
func (s *Service) UpsertPlanBenchmark(ctx context.Context, b domain.PlanBenchmark) error {
	if s.valueStore == nil {
		return fmt.Errorf("value analytics store not configured")
	}
	return s.valueStore.UpsertPlanBenchmark(ctx, b)
}

// DeletePlanBenchmark deletes a custom competitor plan benchmark row.
func (s *Service) DeletePlanBenchmark(ctx context.Context, id string) error {
	if s.valueStore == nil {
		return fmt.Errorf("value analytics store not configured")
	}
	return s.valueStore.DeletePlanBenchmark(ctx, id)
}

// GetPlanValueReport computes API-equivalent USD valuations, monotonic burn-interval cap
// calibrations, per-account/per-model breakdowns, and competitor plan comparisons.
func (s *Service) GetPlanValueReport(ctx context.Context, period domain.MetricPeriod, accountID string) (*domain.PlanValueReport, error) {
	normPeriod, err := NormalizePeriod(period)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	since, windowDays := resolvePeriodWindow(normPeriod, now)

	catalog, err := s.ListPricingCatalog(ctx)
	if err != nil {
		return nil, err
	}
	benchmarks, err := s.ListPlanBenchmarks(ctx)
	if err != nil {
		return nil, err
	}

	accounts, err := s.loadAccountsForReport(ctx, accountID)
	if err != nil {
		return nil, err
	}

	var allSnapshots []*domain.QuotaSnapshot
	calMetricsSince := since
	if s.quotaRepo != nil {
		if snapRepo, ok := s.quotaRepo.(domain.QuotaSnapshotRepository); ok {
			inWindowSnaps, sErr := snapRepo.ListSnapshots(ctx, accountID, since)
			if sErr != nil {
				return nil, sErr
			}
			allSnapshots = append(allSnapshots, inWindowSnaps...)

			if !since.IsZero() && len(inWindowSnaps) > 0 {
				seriesInWindow := make(map[string]bool, len(inWindowSnaps))
				seenIDs := make(map[int64]bool, len(inWindowSnaps))
				for _, snap := range inWindowSnaps {
					if snap == nil {
						continue
					}
					if snap.ID > 0 {
						seenIDs[snap.ID] = true
					}
					seriesInWindow[snap.AccountID+"|"+snapshotSeriesKey(snap)] = true
				}
				anchors, aErr := snapRepo.ListLatestSnapshotsBefore(ctx, accountID, since)
				if aErr != nil {
					return nil, aErr
				}
				minAllowed := since.Add(-7 * 24 * time.Hour)
				for _, a := range anchors {
					if a == nil || (a.ID > 0 && seenIDs[a.ID]) {
						continue
					}
					if !seriesInWindow[a.AccountID+"|"+snapshotSeriesKey(a)] {
						continue
					}
					allSnapshots = append(allSnapshots, a)
					if a.CapturedAt.Before(calMetricsSince) {
						calMetricsSince = a.CapturedAt
					}
				}
				if calMetricsSince.Before(minAllowed) {
					calMetricsSince = minAllowed
				}
			}
		}
	}

	var allMetrics []*domain.TokenMetric
	if qRepo, ok := s.metricsRepo.(domain.TokenMetricQueryRepository); ok {
		allMetrics, err = qRepo.ListMetricsSince(ctx, accountID, calMetricsSince)
		if err != nil {
			return nil, err
		}
	}

	liveBuckets := make(map[string][]*domain.QuotaBucket)
	if s.quotaRepo != nil {
		if allB, lErr := s.quotaRepo.ListAll(ctx); lErr == nil {
			liveBuckets = allB
		}
	}

	if normPeriod == domain.PeriodLifetime {
		windowDays = inferLifetimeWindowDays(allMetrics, now)
	}

	metricsByAcc := make(map[string][]*domain.TokenMetric)
	for _, m := range allMetrics {
		if m != nil {
			metricsByAcc[m.AccountID] = append(metricsByAcc[m.AccountID], m)
		}
	}

	snapsByAcc := make(map[string][]*domain.QuotaSnapshot)
	for _, snap := range allSnapshots {
		if snap != nil {
			snapsByAcc[snap.AccountID] = append(snapsByAcc[snap.AccountID], snap)
		}
	}

	report := &domain.PlanValueReport{
		Period:         normPeriod,
		WindowDays:     windowDays,
		GeneratedAt:    now,
		PricingCatalog: catalog,
		ByAccount:      make([]domain.AccountValueReport, 0, len(accounts)),
		ByModel:        []domain.ModelValueBreakdown{},
		Episodes:       []domain.CalibrationEpisode{},
	}

	globalModelAgg := make(map[string]*modelAccumulator)
	unpricedSet := make(map[string]bool)

	var poolCostUSD, poolRealizedUSD float64
	var poolRequests, poolTokens, poolCachedTokens, poolInputBaseTokens, poolLegacyUnpricedReqs int64
	var poolExternalDrops int

	for _, acc := range accounts {
		accReport, accEpisodes, extDrops, legacyReqs := buildAccountValueReport(
			acc,
			metricsByAcc[acc.ID],
			snapsByAcc[acc.ID],
			liveBuckets[acc.ID],
			catalog,
			since,
			windowDays,
			globalModelAgg,
			unpricedSet,
		)
		report.ByAccount = append(report.ByAccount, accReport)
		report.Episodes = append(report.Episodes, accEpisodes...)
		poolExternalDrops += extDrops
		poolLegacyUnpricedReqs += legacyReqs

		poolCostUSD += acc.MonthlyCostUSD
		poolRealizedUSD += accReport.RealizedAPIUSD
		poolRequests += accReport.TotalRequests
		poolTokens += accReport.TotalTokens
		for _, catSum := range accReport.ByCategory {
			poolCachedTokens += catSum.CachedInputTokens
			poolInputBaseTokens += catSum.UncachedInputTokens + catSum.CachedInputTokens
		}
	}

	report.ByModel = finalizeModelBreakdowns(globalModelAgg)
	unpriced := make([]string, 0, len(unpricedSet))
	for m := range unpricedSet {
		unpriced = append(unpriced, m)
	}
	sort.Strings(unpriced)

	poolSummary := aggregatePoolSummary(
		accounts,
		report.ByAccount,
		poolCostUSD,
		poolRealizedUSD,
		poolRequests,
		poolTokens,
		poolCachedTokens,
		poolInputBaseTokens,
		poolLegacyUnpricedReqs,
		windowDays,
		unpriced,
		poolExternalDrops,
	)
	report.PoolSummary = poolSummary
	report.Benchmarks = appendMeasuredBenchmarks(poolSummary, report.ByAccount, benchmarks)

	return report, nil
}

func (s *Service) loadAccountsForReport(ctx context.Context, accountID string) ([]*domain.Account, error) {
	if s.accountRepo == nil {
		return nil, nil
	}
	if accountID != "" {
		acc, err := s.accountRepo.GetByID(ctx, accountID)
		if err != nil {
			return nil, err
		}
		return []*domain.Account{acc}, nil
	}
	return s.accountRepo.List(ctx)
}

func resolvePeriodWindow(period domain.MetricPeriod, now time.Time) (time.Time, float64) {
	switch period {
	case domain.PeriodDay:
		return now.Add(-24 * time.Hour), 1.0
	case domain.PeriodWeek:
		return now.Add(-7 * 24 * time.Hour), 7.0
	case domain.PeriodMonth:
		return now.Add(-30 * 24 * time.Hour), 30.0
	default:
		return time.Time{}, 30.0
	}
}

func inferLifetimeWindowDays(metrics []*domain.TokenMetric, now time.Time) float64 {
	if len(metrics) == 0 {
		return 30.0
	}
	earliest := metrics[0].Timestamp
	for _, m := range metrics {
		if !m.Timestamp.IsZero() && (earliest.IsZero() || m.Timestamp.Before(earliest)) {
			earliest = m.Timestamp
		}
	}
	if earliest.IsZero() {
		return 30.0
	}
	days := now.Sub(earliest).Hours() / 24.0
	if days < 1.0 {
		return 1.0
	}
	return days
}

type modelAccumulator struct {
	model      string
	category   string
	requests   int64
	uncachedIn int64
	cachedIn   int64
	cacheWrite int64
	output     int64
	thoughts   int64
	total      int64
	usd        float64
	pattern    string
	priced     bool
}

type pricedMetric struct {
	metric   *domain.TokenMetric
	category string
	cost     MetricCostBreakdown
}

func buildAccountValueReport(
	acc *domain.Account,
	metrics []*domain.TokenMetric,
	snaps []*domain.QuotaSnapshot,
	live []*domain.QuotaBucket,
	catalog []domain.ModelPricing,
	since time.Time,
	windowDays float64,
	globalModelAgg map[string]*modelAccumulator,
	unpricedSet map[string]bool,
) (domain.AccountValueReport, []domain.CalibrationEpisode, int, int64) {
	catAgg := map[string]*domain.CategoryValueSummary{
		string(domain.CategoryClaudeGPT): {Category: string(domain.CategoryClaudeGPT)},
		string(domain.CategoryGemini):    {Category: string(domain.CategoryGemini)},
	}
	accModelAgg := make(map[string]*modelAccumulator)
	pricedList := make([]pricedMetric, 0, len(metrics))

	var totalUSD float64
	var totalReqs, totalTokens, totalCached, totalInputBase, legacyUnpricedReqs int64

	for _, m := range metrics {
		if m == nil {
			continue
		}
		cat := string(domain.CategorizeModelID(m.Model))
		pricing, ok := ResolveModelPricing(m.Model, catalog)
		cost := ComputeMetricCost(m, pricing, ok)

		pricedList = append(pricedList, pricedMetric{
			metric:   m,
			category: cat,
			cost:     cost,
		})

		// Pre-window anchor lookback metrics feed interval calibration only;
		// RealizedAPIUSD and per-model breakdowns remain strictly within >= since.
		if !since.IsZero() && m.Timestamp.Before(since) {
			continue
		}
		trimmedModel := strings.TrimSpace(m.Model)
		if !ok {
			if trimmedModel == "" {
				legacyUnpricedReqs++
			} else {
				unpricedSet[m.Model] = true
			}
		}

		totalUSD += cost.APIValueUSD
		totalReqs++
		tok := m.TotalTokens
		if tok == 0 {
			tok = m.PromptTokens + m.CandidatesTokens
		}
		totalTokens += tok
		totalCached += cost.CachedInputTokens
		totalInputBase += cost.UncachedInputTokens + cost.CachedInputTokens

		cs, exists := catAgg[cat]
		if !exists {
			cs = &domain.CategoryValueSummary{Category: cat}
			catAgg[cat] = cs
		}
		cs.Requests++
		cs.UncachedInputTokens += cost.UncachedInputTokens
		cs.CachedInputTokens += cost.CachedInputTokens
		cs.CacheWriteTokens += cost.CacheWriteTokens
		cs.OutputTokens += cost.OutputTokens
		cs.ThoughtsTokens += cost.ThoughtsTokens
		cs.TotalTokens += tok
		cs.APIValueUSD += cost.APIValueUSD

		modelKey := m.Model
		if trimmedModel == "" {
			modelKey = "unknown"
		}
		for _, targetMap := range []map[string]*modelAccumulator{accModelAgg, globalModelAgg} {
			ma, mExists := targetMap[modelKey]
			if !mExists {
				ma = &modelAccumulator{
					model:    modelKey,
					category: cat,
					pattern:  pricing.ModelPattern,
					priced:   ok,
				}
				targetMap[modelKey] = ma
			}
			ma.requests++
			ma.uncachedIn += cost.UncachedInputTokens
			ma.cachedIn += cost.CachedInputTokens
			ma.cacheWrite += cost.CacheWriteTokens
			ma.output += cost.OutputTokens
			ma.thoughts += cost.ThoughtsTokens
			ma.total += tok
			ma.usd += cost.APIValueUSD
		}
	}

	byCat := make(map[string]domain.CategoryValueSummary, len(catAgg))
	for k, cs := range catAgg {
		denom := cs.UncachedInputTokens + cs.CachedInputTokens
		if denom > 0 {
			cs.CacheHitRate = float64(cs.CachedInputTokens) / float64(denom)
		}
		byCat[k] = *cs
	}

	calibrations, episodes, extDrops := calibrateAccountWindows(acc, snaps, live, pricedList, byCat, since)

	var combinedWeeklyPtr *float64
	var combinedMonthlyPtr *float64
	var multPtr *float64

	var weeklySum float64
	hasAnyWeekly := false
	for _, cal := range calibrations {
		if cal.Window == "weekly" && cal.ImpliedFullCapUSD != nil {
			weeklySum += *cal.ImpliedFullCapUSD
			hasAnyWeekly = true
		}
	}
	if hasAnyWeekly {
		combinedWeeklyPtr = float64Ptr(weeklySum)
		estMonthly := weeklySum * weeksPerMonth
		combinedMonthlyPtr = float64Ptr(estMonthly)
		if acc.MonthlyCostUSD > 0 {
			multPtr = float64Ptr(estMonthly / acc.MonthlyCostUSD)
		}
	}

	var realizedMultPtr *float64
	if acc.MonthlyCostUSD > 0 && windowDays > 0 {
		proratedCost := acc.MonthlyCostUSD * (windowDays / 30.0)
		if proratedCost > 0 {
			realizedMultPtr = float64Ptr(totalUSD / proratedCost)
		}
	}

	var cacheRate float64
	if totalInputBase > 0 {
		cacheRate = float64(totalCached) / float64(totalInputBase)
	}

	return domain.AccountValueReport{
		AccountID:                 acc.ID,
		Email:                     acc.Email,
		PlanTier:                  acc.PlanTier,
		MonthlyCostUSD:            acc.MonthlyCostUSD,
		RealizedAPIUSD:            totalUSD,
		TotalRequests:             totalReqs,
		TotalTokens:               totalTokens,
		CacheHitRate:              cacheRate,
		Calibrations:              calibrations,
		ImpliedWeeklyCombinedUSD:  combinedWeeklyPtr,
		ImpliedMonthlyCombinedUSD: combinedMonthlyPtr,
		ValueMultiplier:           multPtr,
		RealizedMultiplier:        realizedMultPtr,
		ByCategory:                byCat,
		ByModel:                   finalizeModelBreakdowns(accModelAgg),
	}, episodes, extDrops, legacyUnpricedReqs
}

func snapshotSeriesKey(s *domain.QuotaSnapshot) string {
	cat := normalizeSnapshotCategory(s.Category, s.BucketID, s.DisplayName)
	win := domain.NormalizeQuotaWindow(s.Window, s.BucketID)
	if cat != string(domain.CategoryUnknown) && cat != "" {
		return cat + "|" + win
	}
	return "raw|" + s.BucketID + "|" + win
}

type windowCalKey struct {
	category string
	window   string
}

type windowCalAccum struct {
	sumUSD        float64
	sumDelta      float64
	intervals     int
	externalDrops int
	epochs        map[string]bool
	tokens        int64
	cachedTokens  int64
	inputBase     int64
}

func calibrateAccountWindows(
	acc *domain.Account,
	snaps []*domain.QuotaSnapshot,
	live []*domain.QuotaBucket,
	pricedList []pricedMetric,
	byCat map[string]domain.CategoryValueSummary,
	since time.Time,
) ([]domain.WindowCalibration, []domain.CalibrationEpisode, int) {
	bySeries := make(map[string][]*domain.QuotaSnapshot)
	for _, s := range snaps {
		if s == nil || strings.EqualFold(s.Source, "synthetic") {
			continue
		}
		key := snapshotSeriesKey(s)
		bySeries[key] = append(bySeries[key], s)
	}

	accumMap := make(map[windowCalKey]*windowCalAccum)
	var episodes []domain.CalibrationEpisode
	totalExternalDrops := 0

	for _, list := range bySeries {
		sort.Slice(list, func(i, j int) bool {
			if !list[i].CapturedAt.Equal(list[j].CapturedAt) {
				return list[i].CapturedAt.Before(list[j].CapturedAt)
			}
			return list[i].ID < list[j].ID
		})
		for i := 1; i < len(list); i++ {
			prev := list[i-1]
			curr := list[i]
			if !since.IsZero() && curr.CapturedAt.Before(since) {
				continue
			}
			delta := prev.RemainingFraction - curr.RemainingFraction
			if delta <= 1e-6 {
				continue
			}
			if !isSameResetEpoch(prev, curr) {
				continue
			}

			cat := normalizeSnapshotCategory(curr.Category, curr.BucketID, curr.DisplayName)
			win := domain.NormalizeQuotaWindow(curr.Window, curr.BucketID)
			key := windowCalKey{category: cat, window: win}
			ac := accumMap[key]
			if ac == nil {
				ac = &windowCalAccum{epochs: make(map[string]bool)}
				accumMap[key] = ac
			}

			var intervalUSD float64
			var intervalReqs, intervalUncached, intervalCached, intervalOut, intervalThoughts, intervalTokens int64
			for _, pm := range pricedList {
				if pm.category != cat {
					continue
				}
				ts := pm.metric.Timestamp
				if ts.After(prev.CapturedAt) && !ts.After(curr.CapturedAt) {
					intervalUSD += pm.cost.APIValueUSD
					intervalReqs++
					intervalUncached += pm.cost.UncachedInputTokens
					intervalCached += pm.cost.CachedInputTokens
					intervalOut += pm.cost.OutputTokens
					intervalThoughts += pm.cost.ThoughtsTokens
					tok := pm.metric.TotalTokens
					if tok == 0 {
						tok = pm.metric.PromptTokens + pm.metric.CandidatesTokens
					}
					intervalTokens += tok
				}
			}

			if intervalReqs == 0 {
				ac.externalDrops++
				totalExternalDrops++
				continue
			}

			ac.sumUSD += intervalUSD
			ac.sumDelta += delta
			ac.intervals++
			ac.tokens += intervalTokens
			ac.cachedTokens += intervalCached
			ac.inputBase += intervalUncached + intervalCached
			epochKey := curr.ResetTime.UTC().Truncate(10 * time.Minute).Format(time.RFC3339)
			if curr.ResetTime.IsZero() {
				epochKey = prev.CapturedAt.UTC().Truncate(time.Hour).Format(time.RFC3339)
			}
			ac.epochs[epochKey] = true

			episodes = append(episodes, domain.CalibrationEpisode{
				AccountID:           acc.ID,
				Email:               acc.Email,
				PlanTier:            acc.PlanTier,
				Category:            cat,
				Window:              win,
				BucketID:            curr.BucketID,
				StartTime:           prev.CapturedAt,
				EndTime:             curr.CapturedAt,
				ResetTime:           curr.ResetTime,
				StartFraction:       prev.RemainingFraction,
				EndFraction:         curr.RemainingFraction,
				DeltaFraction:       delta,
				SourceStart:         prev.Source,
				SourceEnd:           curr.Source,
				Requests:            intervalReqs,
				UncachedInputTokens: intervalUncached,
				CachedInputTokens:   intervalCached,
				OutputTokens:        intervalOut,
				ThoughtsTokens:      intervalThoughts,
				APIValueUSD:         intervalUSD,
				Implied100PctUSD:    intervalUSD / delta,
			})
		}
	}

	liveByKey := make(map[windowCalKey]*domain.QuotaBucket)
	for _, b := range live {
		if b == nil || strings.EqualFold(b.Source, "synthetic") {
			continue
		}
		cat := string(domain.CategorizeBucket(b.BucketID, b.DisplayName))
		win := domain.NormalizeQuotaWindow(string(b.Window), b.BucketID)
		liveByKey[windowCalKey{category: cat, window: win}] = b
	}

	standardKeys := []windowCalKey{
		{category: string(domain.CategoryClaudeGPT), window: "5h"},
		{category: string(domain.CategoryClaudeGPT), window: "weekly"},
		{category: string(domain.CategoryGemini), window: "5h"},
		{category: string(domain.CategoryGemini), window: "weekly"},
	}

	calibrations := make([]domain.WindowCalibration, 0, len(standardKeys))
	for _, key := range standardKeys {
		ac := accumMap[key]
		cal := domain.WindowCalibration{
			Category:       key.category,
			Window:         key.window,
			RealizedAPIUSD: byCat[key.category].APIValueUSD,
			Confidence:     "none",
		}
		if ac != nil {
			cal.ObservedIntervalUSD = ac.sumUSD
			cal.ObservedDeltaFraction = ac.sumDelta
			cal.IntervalsUsed = ac.intervals
			cal.ExternalDropIntervals = ac.externalDrops
			cal.EpochsObserved = len(ac.epochs)
			cal.TokensInIntervals = ac.tokens
			if ac.inputBase > 0 {
				cal.CacheHitRate = float64(ac.cachedTokens) / float64(ac.inputBase)
			}
			cal.Confidence = calibrationConfidence(ac.sumDelta)
			if ac.sumDelta >= 0.01 {
				capUSD := ac.sumUSD / ac.sumDelta
				cal.ImpliedFullCapUSD = float64Ptr(capUSD)
				cal.CostPerPercentUSD = float64Ptr(capUSD / 100.0)
			}
		}
		if lb := liveByKey[key]; lb != nil {
			cal.CurrentRemainingFraction = float64Ptr(lb.RemainingFraction)
			if !lb.ResetTime.IsZero() {
				rt := lb.ResetTime
				cal.CurrentResetTime = &rt
			}
		}
		calibrations = append(calibrations, cal)
	}

	return calibrations, episodes, totalExternalDrops
}

func isSameResetEpoch(prev, curr *domain.QuotaSnapshot) bool {
	if !prev.ResetTime.IsZero() && !curr.ResetTime.IsZero() {
		diff := curr.ResetTime.Sub(prev.ResetTime)
		if diff < 0 {
			diff = -diff
		}
		return diff <= 5*time.Minute
	}
	if prev.ResetTime.IsZero() && curr.ResetTime.IsZero() {
		return true
	}
	// Allow initial activation from 100% -> <100% or 429 exhaustion edge.
	return curr.Source == "429_exhaustion" || prev.RemainingFraction >= 0.999
}

func normalizeSnapshotCategory(cat, bucketID, displayName string) string {
	if cat == string(domain.CategoryClaudeGPT) || cat == string(domain.CategoryGemini) {
		return cat
	}
	return string(domain.CategorizeBucket(bucketID, displayName))
}

func calibrationConfidence(delta float64) string {
	switch {
	case delta >= 0.40:
		return "high"
	case delta >= 0.10:
		return "medium"
	case delta >= 0.01:
		return "low"
	default:
		return "none"
	}
}

func finalizeModelBreakdowns(m map[string]*modelAccumulator) []domain.ModelValueBreakdown {
	out := make([]domain.ModelValueBreakdown, 0, len(m))
	for _, a := range m {
		var cacheRate float64
		denom := a.uncachedIn + a.cachedIn
		if denom > 0 {
			cacheRate = float64(a.cachedIn) / float64(denom)
		}
		out = append(out, domain.ModelValueBreakdown{
			Model:               a.model,
			Category:            a.category,
			Requests:            a.requests,
			UncachedInputTokens: a.uncachedIn,
			CachedInputTokens:   a.cachedIn,
			CacheWriteTokens:    a.cacheWrite,
			OutputTokens:        a.output,
			ThoughtsTokens:      a.thoughts,
			TotalTokens:         a.total,
			CacheHitRate:        cacheRate,
			APIValueUSD:         a.usd,
			PricingPattern:      a.pattern,
			Priced:              a.priced,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if math.Abs(out[i].APIValueUSD-out[j].APIValueUSD) > 1e-9 {
			return out[i].APIValueUSD > out[j].APIValueUSD
		}
		return out[i].TotalTokens > out[j].TotalTokens
	})
	return out
}

func cohortSeatUnitCost(accounts []*domain.Account, aiProCount int) float64 {
	var sum float64
	var cnt int
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		if aiProCount > 0 && acc.PlanTier != "" && acc.PlanTier != "ai_pro" {
			continue
		}
		sum += acc.MonthlyCostUSD
		cnt++
	}
	if cnt > 0 && sum > 0 {
		return sum / float64(cnt)
	}
	return 20.0
}

func cohortSeatUnitCostFromReports(reports []domain.AccountValueReport, aiProCount int) float64 {
	var sum float64
	var cnt int
	for _, r := range reports {
		if aiProCount > 0 && r.PlanTier != "" && r.PlanTier != "ai_pro" {
			continue
		}
		sum += r.MonthlyCostUSD
		cnt++
	}
	if cnt > 0 && sum > 0 {
		return sum / float64(cnt)
	}
	return 20.0
}

func aggregatePoolSummary(
	accounts []*domain.Account,
	reports []domain.AccountValueReport,
	poolCostUSD, poolRealizedUSD float64,
	poolRequests, poolTokens, poolCachedTokens, poolInputBaseTokens, legacyUnpricedReqs int64,
	windowDays float64,
	unpriced []string,
	extDrops int,
) domain.PoolValueSummary {
	aiProCount := 0
	for _, acc := range accounts {
		if acc.PlanTier == "" || acc.PlanTier == "ai_pro" {
			aiProCount++
		}
	}

	avgWindow := func(cat, win string) (mleCap *float64, minCap *float64, maxCap *float64, conf string, totalDelta float64) {
		var sumUSD float64
		var seatCaps []float64
		for _, r := range reports {
			if aiProCount > 0 && r.PlanTier != "" && r.PlanTier != "ai_pro" {
				continue
			}
			for _, cal := range r.Calibrations {
				if cal.Category == cat && cal.Window == win {
					sumUSD += cal.ObservedIntervalUSD
					totalDelta += cal.ObservedDeltaFraction
					if cal.ImpliedFullCapUSD != nil {
						seatCaps = append(seatCaps, *cal.ImpliedFullCapUSD)
					}
				}
			}
		}
		conf = calibrationConfidence(totalDelta)
		if len(seatCaps) >= 2 {
			minV, maxV := seatCaps[0], seatCaps[0]
			for _, v := range seatCaps[1:] {
				if v < minV {
					minV = v
				}
				if v > maxV {
					maxV = v
				}
			}
			minCap = float64Ptr(minV)
			maxCap = float64Ptr(maxV)
		}
		if totalDelta < 0.01 {
			return nil, minCap, maxCap, conf, totalDelta
		}
		return float64Ptr(sumUSD / totalDelta), minCap, maxCap, conf, totalDelta
	}

	c5h, _, _, _, _ := avgWindow(string(domain.CategoryClaudeGPT), "5h")
	cWk, cMin, cMax, cConf, cDelta := avgWindow(string(domain.CategoryClaudeGPT), "weekly")
	g5h, _, _, _, _ := avgWindow(string(domain.CategoryGemini), "5h")
	gWk, gMin, gMax, gConf, gDelta := avgWindow(string(domain.CategoryGemini), "weekly")

	var combinedWk, combinedMo, combinedMult *float64
	if cWk != nil || gWk != nil {
		var wk float64
		if cWk != nil {
			wk += *cWk
		}
		if gWk != nil {
			wk += *gWk
		}
		combinedWk = float64Ptr(wk)
		mo := wk * weeksPerMonth
		combinedMo = float64Ptr(mo)
		unitCost := cohortSeatUnitCost(accounts, aiProCount)
		if unitCost > 0 {
			combinedMult = float64Ptr(mo / unitCost)
		}
	}

	var realizedMult *float64
	if poolCostUSD > 0 && windowDays > 0 {
		prorated := poolCostUSD * (windowDays / 30.0)
		if prorated > 0 {
			realizedMult = float64Ptr(poolRealizedUSD / prorated)
		}
	}

	var cacheRate float64
	if poolInputBaseTokens > 0 {
		cacheRate = float64(poolCachedTokens) / float64(poolInputBaseTokens)
	}

	return domain.PoolValueSummary{
		TotalAccounts:             len(accounts),
		AIProAccounts:             aiProCount,
		TotalMonthlyCostUSD:       poolCostUSD,
		RealizedAPIUSD:            poolRealizedUSD,
		RealizedMultiplier:        realizedMult,
		TotalRequests:             poolRequests,
		TotalTokens:               poolTokens,
		CacheHitRate:              cacheRate,
		AvgClaude5hCapUSD:         c5h,
		AvgClaudeWeeklyCapUSD:     cWk,
		MinClaudeWeeklyCapUSD:     cMin,
		MaxClaudeWeeklyCapUSD:     cMax,
		ClaudeWeeklyConfidence:    cConf,
		ClaudeObservedDelta:       cDelta,
		AvgGemini5hCapUSD:         g5h,
		AvgGeminiWeeklyCapUSD:     gWk,
		MinGeminiWeeklyCapUSD:     gMin,
		MaxGeminiWeeklyCapUSD:     gMax,
		GeminiWeeklyConfidence:    gConf,
		GeminiObservedDelta:       gDelta,
		AvgCombinedWeeklyCapUSD:   combinedWk,
		AvgCombinedMonthlyCapUSD:  combinedMo,
		CombinedValueMultiplier:   combinedMult,
		UnpricedModels:            unpriced,
		LegacyUnpricedRequests:    legacyUnpricedReqs,
		ExternalDropIntervalCount: extDrops,
	}
}

func appendMeasuredBenchmarks(pool domain.PoolValueSummary, reports []domain.AccountValueReport, benchmarks []domain.PlanBenchmark) []domain.PlanBenchmark {
	out := make([]domain.PlanBenchmark, 0, len(benchmarks)+3)

	unitCost := cohortSeatUnitCostFromReports(reports, pool.AIProAccounts)
	if unitCost <= 0 && pool.TotalAccounts > 0 && pool.TotalMonthlyCostUSD > 0 {
		unitCost = pool.TotalMonthlyCostUSD / float64(pool.TotalAccounts)
	}

	addMeasured := func(id, name string, w5h, wk *float64, conf, notes string) {
		var v5h, vWk float64
		if w5h != nil {
			v5h = *w5h
		}
		if wk != nil {
			vWk = *wk
		}
		b := finalizeBenchmark(domain.PlanBenchmark{
			ID:             id,
			Provider:       "Google AGY (Measured)",
			PlanName:       name,
			MonthlyCostUSD: unitCost,
			Window5hAPIUSD: v5h,
			WeeklyAPIUSD:   vWk,
			Notes:          notes,
			IsMeasured:     true,
			Confidence:     conf,
		})
		out = append(out, b)
	}

	addMeasured(
		"agy-ai-pro-claude",
		"AGY AI Pro (Claude/GPT pool)",
		pool.AvgClaude5hCapUSD,
		pool.AvgClaudeWeeklyCapUSD,
		pool.ClaudeWeeklyConfidence,
		"Measured from your proxy token mix + quota deltas",
	)
	addMeasured(
		"agy-ai-pro-gemini",
		"AGY AI Pro (Gemini pool)",
		pool.AvgGemini5hCapUSD,
		pool.AvgGeminiWeeklyCapUSD,
		pool.GeminiWeeklyConfidence,
		"Measured from your proxy token mix + quota deltas",
	)

	var comb5hPtr *float64
	if pool.AvgClaude5hCapUSD != nil || pool.AvgGemini5hCapUSD != nil {
		var sum5h float64
		if pool.AvgClaude5hCapUSD != nil {
			sum5h += *pool.AvgClaude5hCapUSD
		}
		if pool.AvgGemini5hCapUSD != nil {
			sum5h += *pool.AvgGemini5hCapUSD
		}
		comb5hPtr = float64Ptr(sum5h)
	}
	combConf := pool.ClaudeWeeklyConfidence
	if combConf == "none" {
		combConf = pool.GeminiWeeklyConfidence
	}
	addMeasured(
		"agy-ai-pro-combined",
		"AGY AI Pro (Combined 2-pool)",
		comb5hPtr,
		pool.AvgCombinedWeeklyCapUSD,
		combConf,
		"Combined Claude/GPT + Gemini pools per AI Pro seat",
	)

	out = append(out, benchmarks...)
	return out
}

func float64Ptr(v float64) *float64 {
	return &v
}
