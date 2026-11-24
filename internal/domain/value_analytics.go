package domain

import (
	"context"
	"time"
)

// ModelPricing defines official API token rates in USD per 1 million tokens.
type ModelPricing struct {
	ModelPattern          string  `json:"model_pattern"`
	DisplayName           string  `json:"display_name"`
	InputPerMillion       float64 `json:"input_per_million"`
	CachedInputPerMillion float64 `json:"cached_input_per_million"`
	CacheWritePerMillion  float64 `json:"cache_write_per_million"`
	OutputPerMillion      float64 `json:"output_per_million"`
	IsOverride            bool    `json:"is_override,omitempty"`
}

// PlanBenchmark represents a reference subscription plan (or synthesized AGY row)
// normalized onto API-equivalent USD value per 5h window, week, and month.
type PlanBenchmark struct {
	ID             string  `json:"id"`
	Provider       string  `json:"provider"`
	PlanName       string  `json:"plan_name"`
	MonthlyCostUSD float64 `json:"monthly_cost_usd"`
	Window5hAPIUSD float64 `json:"window_5h_api_usd"`
	WeeklyAPIUSD   float64 `json:"weekly_api_usd"`
	EstMonthlyUSD  float64 `json:"est_monthly_api_usd"`
	ValuePerDollar float64 `json:"value_per_dollar"`
	Notes          string  `json:"notes,omitempty"`
	IsCustom       bool    `json:"is_custom,omitempty"`
	IsMeasured     bool    `json:"is_measured,omitempty"`
	Confidence     string  `json:"confidence,omitempty"`
}

// ValueAnalyticsRepository persists user overrides for model API pricing and competitor plan benchmarks.
type ValueAnalyticsRepository interface {
	ListPricingOverrides(ctx context.Context) ([]ModelPricing, error)
	UpsertPricingOverride(ctx context.Context, p ModelPricing) error
	DeletePricingOverride(ctx context.Context, pattern string) error
	ListPlanBenchmarks(ctx context.Context) ([]PlanBenchmark, error)
	UpsertPlanBenchmark(ctx context.Context, b PlanBenchmark) error
	DeletePlanBenchmark(ctx context.Context, id string) error
}

// WindowCalibration reports empirical quota-burn calibration for one (category, window) bucket.
type WindowCalibration struct {
	Category                 string     `json:"category"`
	Window                   string     `json:"window"`
	RealizedAPIUSD           float64    `json:"realized_api_usd"`
	ObservedDeltaFraction    float64    `json:"observed_delta_fraction"`
	ImpliedFullCapUSD        *float64   `json:"implied_full_cap_usd,omitempty"`
	Confidence               string     `json:"confidence"`
	IntervalsUsed            int        `json:"intervals_used"`
	ExternalDropIntervals    int        `json:"external_drop_intervals,omitempty"`
	EpochsObserved           int        `json:"epochs_observed"`
	CostPerPercentUSD        *float64   `json:"cost_per_percent_usd,omitempty"`
	TokensInIntervals        int64      `json:"tokens_in_intervals"`
	CacheHitRate             float64    `json:"cache_hit_rate"`
	CurrentRemainingFraction *float64   `json:"current_remaining_fraction,omitempty"`
	CurrentResetTime         *time.Time `json:"current_reset_time,omitempty"`
}

// CategoryValueSummary aggregates realized token usage and API value for one model category.
type CategoryValueSummary struct {
	Category            string  `json:"category"`
	Requests            int64   `json:"requests"`
	UncachedInputTokens int64   `json:"uncached_input_tokens"`
	CachedInputTokens   int64   `json:"cached_input_tokens"`
	CacheWriteTokens    int64   `json:"cache_write_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ThoughtsTokens      int64   `json:"thoughts_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	CacheHitRate        float64 `json:"cache_hit_rate"`
	APIValueUSD         float64 `json:"api_value_usd"`
}

// ModelValueBreakdown breaks down realized usage and API-equivalent value by model.
type ModelValueBreakdown struct {
	Model               string  `json:"model"`
	Category            string  `json:"category"`
	Requests            int64   `json:"requests"`
	UncachedInputTokens int64   `json:"uncached_input_tokens"`
	CachedInputTokens   int64   `json:"cached_input_tokens"`
	CacheWriteTokens    int64   `json:"cache_write_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ThoughtsTokens      int64   `json:"thoughts_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	CacheHitRate        float64 `json:"cache_hit_rate"`
	APIValueUSD         float64 `json:"api_value_usd"`
	PricingPattern      string  `json:"pricing_pattern,omitempty"`
	Priced              bool    `json:"priced"`
}

// AccountValueReport summarizes realized value and calibrated plan caps for one account.
type AccountValueReport struct {
	AccountID                 string                          `json:"account_id"`
	Email                     string                          `json:"email"`
	PlanTier                  string                          `json:"plan_tier"`
	MonthlyCostUSD            float64                         `json:"monthly_cost_usd"`
	RealizedAPIUSD            float64                         `json:"realized_api_usd"`
	TotalRequests             int64                           `json:"total_requests"`
	TotalTokens               int64                           `json:"total_tokens"`
	CacheHitRate              float64                         `json:"cache_hit_rate"`
	Calibrations              []WindowCalibration             `json:"calibrations"`
	ImpliedWeeklyCombinedUSD  *float64                        `json:"implied_weekly_combined_usd,omitempty"`
	ImpliedMonthlyCombinedUSD *float64                        `json:"implied_monthly_combined_usd,omitempty"`
	ValueMultiplier           *float64                        `json:"value_multiplier,omitempty"`
	RealizedMultiplier        *float64                        `json:"realized_multiplier,omitempty"`
	ByCategory                map[string]CategoryValueSummary `json:"by_category"`
	ByModel                   []ModelValueBreakdown           `json:"by_model"`
}

// CalibrationEpisode captures one monotonic burn interval between consecutive quota snapshots.
type CalibrationEpisode struct {
	AccountID           string    `json:"account_id"`
	Email               string    `json:"email"`
	PlanTier            string    `json:"plan_tier"`
	Category            string    `json:"category"`
	Window              string    `json:"window"`
	BucketID            string    `json:"bucket_id"`
	StartTime           time.Time `json:"start_time"`
	EndTime             time.Time `json:"end_time"`
	ResetTime           time.Time `json:"reset_time,omitempty"`
	StartFraction       float64   `json:"start_fraction"`
	EndFraction         float64   `json:"end_fraction"`
	DeltaFraction       float64   `json:"delta_fraction"`
	SourceStart         string    `json:"source_start"`
	SourceEnd           string    `json:"source_end"`
	Requests            int64     `json:"requests"`
	UncachedInputTokens int64     `json:"uncached_input_tokens"`
	CachedInputTokens   int64     `json:"cached_input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	ThoughtsTokens      int64     `json:"thoughts_tokens"`
	APIValueUSD         float64   `json:"api_value_usd"`
	Implied100PctUSD    float64   `json:"implied_100pct_usd"`
}

// PoolValueSummary aggregates valuation and cap calibration across all accounts in the pool.
type PoolValueSummary struct {
	TotalAccounts             int      `json:"total_accounts"`
	AIProAccounts             int      `json:"ai_pro_accounts"`
	TotalMonthlyCostUSD       float64  `json:"total_monthly_cost_usd"`
	RealizedAPIUSD            float64  `json:"realized_api_usd"`
	RealizedMultiplier        *float64 `json:"realized_multiplier,omitempty"`
	TotalRequests             int64    `json:"total_requests"`
	TotalTokens               int64    `json:"total_tokens"`
	CacheHitRate              float64  `json:"cache_hit_rate"`
	AvgClaude5hCapUSD         *float64 `json:"avg_claude_5h_cap_usd,omitempty"`
	AvgClaudeWeeklyCapUSD     *float64 `json:"avg_claude_weekly_cap_usd,omitempty"`
	ClaudeWeeklyConfidence    string   `json:"claude_weekly_confidence"`
	ClaudeObservedDelta       float64  `json:"claude_observed_delta"`
	AvgGemini5hCapUSD         *float64 `json:"avg_gemini_5h_cap_usd,omitempty"`
	AvgGeminiWeeklyCapUSD     *float64 `json:"avg_gemini_weekly_cap_usd,omitempty"`
	GeminiWeeklyConfidence    string   `json:"gemini_weekly_confidence"`
	GeminiObservedDelta       float64  `json:"gemini_observed_delta"`
	AvgCombinedWeeklyCapUSD   *float64 `json:"avg_combined_weekly_cap_usd,omitempty"`
	AvgCombinedMonthlyCapUSD  *float64 `json:"avg_combined_monthly_cap_usd,omitempty"`
	CombinedValueMultiplier   *float64 `json:"combined_value_multiplier,omitempty"`
	UnpricedModels            []string `json:"unpriced_models,omitempty"`
	ExternalDropIntervalCount int      `json:"external_drop_interval_count,omitempty"`
}

// PlanValueReport is the complete response payload for GET /api/analytics/value.
type PlanValueReport struct {
	Period         MetricPeriod          `json:"period"`
	WindowDays     float64               `json:"window_days"`
	GeneratedAt    time.Time             `json:"generated_at"`
	PoolSummary    PoolValueSummary      `json:"pool_summary"`
	ByAccount      []AccountValueReport  `json:"by_account"`
	ByModel        []ModelValueBreakdown `json:"by_model"`
	Benchmarks     []PlanBenchmark       `json:"benchmarks"`
	PricingCatalog []ModelPricing        `json:"pricing_catalog"`
	Episodes       []CalibrationEpisode  `json:"episodes"`
}

// ValueAnalyticsService computes plan valuation, cap calibrations, and manages pricing/benchmarks.
type ValueAnalyticsService interface {
	GetPlanValueReport(ctx context.Context, period MetricPeriod, accountID string) (*PlanValueReport, error)
	ListPricingCatalog(ctx context.Context) ([]ModelPricing, error)
	UpsertPricingOverride(ctx context.Context, p ModelPricing) error
	DeletePricingOverride(ctx context.Context, pattern string) error
	ListPlanBenchmarks(ctx context.Context) ([]PlanBenchmark, error)
	UpsertPlanBenchmark(ctx context.Context, b PlanBenchmark) error
	DeletePlanBenchmark(ctx context.Context, id string) error
}
