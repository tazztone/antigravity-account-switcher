package metrics

import (
	"sort"
	"strings"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

const weeksPerMonth = 30.0 / 7.0

var defaultModelPricingCatalog = []domain.ModelPricing{
	// Anthropic Claude Opus
	{ModelPattern: "claude-opus-5-5", DisplayName: "Claude Opus 5.5", InputPerMillion: 5.00, CachedInputPerMillion: 0.50, CacheWritePerMillion: 6.25, OutputPerMillion: 25.00},
	{ModelPattern: "claude-opus-4-6", DisplayName: "Claude Opus 4.6", InputPerMillion: 5.00, CachedInputPerMillion: 0.50, CacheWritePerMillion: 6.25, OutputPerMillion: 25.00},
	{ModelPattern: "claude-opus-4-5", DisplayName: "Claude Opus 4.5", InputPerMillion: 5.00, CachedInputPerMillion: 0.50, CacheWritePerMillion: 6.25, OutputPerMillion: 25.00},
	{ModelPattern: "claude-opus-4-1", DisplayName: "Claude Opus 4.1", InputPerMillion: 15.00, CachedInputPerMillion: 1.50, CacheWritePerMillion: 18.75, OutputPerMillion: 75.00},
	{ModelPattern: "claude-opus-4-0", DisplayName: "Claude Opus 4.0", InputPerMillion: 15.00, CachedInputPerMillion: 1.50, CacheWritePerMillion: 18.75, OutputPerMillion: 75.00},
	{ModelPattern: "claude-opus", DisplayName: "Claude Opus", InputPerMillion: 5.00, CachedInputPerMillion: 0.50, CacheWritePerMillion: 6.25, OutputPerMillion: 25.00},
	// Anthropic Claude Sonnet & Haiku
	{ModelPattern: "claude-sonnet", DisplayName: "Claude Sonnet", InputPerMillion: 3.00, CachedInputPerMillion: 0.30, CacheWritePerMillion: 3.75, OutputPerMillion: 15.00},
	{ModelPattern: "claude-haiku", DisplayName: "Claude Haiku", InputPerMillion: 1.00, CachedInputPerMillion: 0.10, CacheWritePerMillion: 1.25, OutputPerMillion: 5.00},
	// OpenAI / GPT-OSS
	{ModelPattern: "gpt-oss-120b", DisplayName: "GPT-OSS 120B", InputPerMillion: 0.15, CachedInputPerMillion: 0.04, CacheWritePerMillion: 0.00, OutputPerMillion: 0.60},
	// Google Gemini Pro & 4-series tiers (90% cache discount; 2x input/cached, 1.5x output above 200k prompt tokens)
	{ModelPattern: "gemini-4-hydrogen", DisplayName: "Gemini 4 Hydrogen", InputPerMillion: 1.25, CachedInputPerMillion: 0.125, CacheWritePerMillion: 0.00, OutputPerMillion: 10.00, LongContextThreshold: 200_000, LongContextInputMultiplier: 2.0, LongContextOutputMultiplier: 1.5},
	{ModelPattern: "gemini-4-neon", DisplayName: "Gemini 4 Neon", InputPerMillion: 1.25, CachedInputPerMillion: 0.125, CacheWritePerMillion: 0.00, OutputPerMillion: 10.00, LongContextThreshold: 200_000, LongContextInputMultiplier: 2.0, LongContextOutputMultiplier: 1.5},
	{ModelPattern: "gemini-4-carbon", DisplayName: "Gemini 4 Carbon", InputPerMillion: 1.25, CachedInputPerMillion: 0.125, CacheWritePerMillion: 0.00, OutputPerMillion: 10.00, LongContextThreshold: 200_000, LongContextInputMultiplier: 2.0, LongContextOutputMultiplier: 1.5},
	{ModelPattern: "gemini-4-argon", DisplayName: "Gemini 4 Argon", InputPerMillion: 1.25, CachedInputPerMillion: 0.125, CacheWritePerMillion: 0.00, OutputPerMillion: 10.00, LongContextThreshold: 200_000, LongContextInputMultiplier: 2.0, LongContextOutputMultiplier: 1.5},
	{ModelPattern: "gemini-3-pro", DisplayName: "Gemini 3 Pro", InputPerMillion: 1.25, CachedInputPerMillion: 0.125, CacheWritePerMillion: 0.00, OutputPerMillion: 10.00, LongContextThreshold: 200_000, LongContextInputMultiplier: 2.0, LongContextOutputMultiplier: 1.5},
	{ModelPattern: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", InputPerMillion: 1.25, CachedInputPerMillion: 0.125, CacheWritePerMillion: 0.00, OutputPerMillion: 10.00, LongContextThreshold: 200_000, LongContextInputMultiplier: 2.0, LongContextOutputMultiplier: 1.5},
	// Google Gemini Flash & Flash-Lite (90% cache discount, flat across context window)
	{ModelPattern: "gemini-3.5-flash-lite", DisplayName: "Gemini 3.5 Flash Lite", InputPerMillion: 0.10, CachedInputPerMillion: 0.01, CacheWritePerMillion: 0.00, OutputPerMillion: 0.40},
	{ModelPattern: "gemini-2.5-flash-lite", DisplayName: "Gemini 2.5 Flash Lite", InputPerMillion: 0.10, CachedInputPerMillion: 0.01, CacheWritePerMillion: 0.00, OutputPerMillion: 0.40},
	{ModelPattern: "flash-lite", DisplayName: "Gemini Flash Lite", InputPerMillion: 0.10, CachedInputPerMillion: 0.01, CacheWritePerMillion: 0.00, OutputPerMillion: 0.40},
	{ModelPattern: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", InputPerMillion: 0.30, CachedInputPerMillion: 0.03, CacheWritePerMillion: 0.00, OutputPerMillion: 2.50},
	{ModelPattern: "gemini-3-flash", DisplayName: "Gemini 3 Flash", InputPerMillion: 0.30, CachedInputPerMillion: 0.03, CacheWritePerMillion: 0.00, OutputPerMillion: 2.50},
	{ModelPattern: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", InputPerMillion: 0.30, CachedInputPerMillion: 0.03, CacheWritePerMillion: 0.00, OutputPerMillion: 2.50},
}

var defaultPlanBenchmarks = []domain.PlanBenchmark{
	{
		ID:             "claude-pro",
		Provider:       "Anthropic",
		PlanName:       "Claude Pro",
		MonthlyCostUSD: 20.0,
		Window5hAPIUSD: 10.0,
		WeeklyAPIUSD:   50.0,
		Notes:          "Editable reference baseline (Wokey.ai / SemiAnalysis style)",
	},
	{
		ID:             "claude-max-5x",
		Provider:       "Anthropic",
		PlanName:       "Claude Max 5x",
		MonthlyCostUSD: 100.0,
		Window5hAPIUSD: 50.0,
		WeeklyAPIUSD:   250.0,
		Notes:          "Editable reference baseline",
	},
	{
		ID:             "claude-max-20x",
		Provider:       "Anthropic",
		PlanName:       "Claude Max 20x",
		MonthlyCostUSD: 200.0,
		Window5hAPIUSD: 100.0,
		WeeklyAPIUSD:   500.0,
		Notes:          "Editable reference baseline",
	},
	{
		ID:             "codex-plus",
		Provider:       "OpenAI",
		PlanName:       "ChatGPT Plus / Codex",
		MonthlyCostUSD: 20.0,
		Window5hAPIUSD: 5.0,
		WeeklyAPIUSD:   20.0,
		Notes:          "Editable reference baseline",
	},
	{
		ID:             "codex-pro",
		Provider:       "OpenAI",
		PlanName:       "ChatGPT Pro / Codex",
		MonthlyCostUSD: 200.0,
		Window5hAPIUSD: 25.0,
		WeeklyAPIUSD:   100.0,
		Notes:          "Editable reference baseline",
	},
}

// DefaultModelPricing returns a copy of the built-in official API pricing catalog.
func DefaultModelPricing() []domain.ModelPricing {
	out := make([]domain.ModelPricing, len(defaultModelPricingCatalog))
	copy(out, defaultModelPricingCatalog)
	return out
}

// DefaultPlanBenchmarks returns a copy of the built-in competitor reference benchmarks.
func DefaultPlanBenchmarks() []domain.PlanBenchmark {
	out := make([]domain.PlanBenchmark, len(defaultPlanBenchmarks))
	for i, b := range defaultPlanBenchmarks {
		out[i] = finalizeBenchmark(b)
	}
	return out
}

func finalizeBenchmark(b domain.PlanBenchmark) domain.PlanBenchmark {
	b.EstMonthlyUSD = b.WeeklyAPIUSD * weeksPerMonth
	if b.MonthlyCostUSD > 0 {
		b.ValuePerDollar = b.EstMonthlyUSD / b.MonthlyCostUSD
	}
	return b
}

// MergePricingCatalog merges user overrides over built-in pricing defaults.
func MergePricingCatalog(overrides []domain.ModelPricing) []domain.ModelPricing {
	byPattern := make(map[string]domain.ModelPricing, len(defaultModelPricingCatalog)+len(overrides))
	for _, p := range defaultModelPricingCatalog {
		byPattern[p.ModelPattern] = p
	}
	for _, o := range overrides {
		pat := strings.ToLower(strings.TrimSpace(o.ModelPattern))
		if pat == "" {
			continue
		}
		o.ModelPattern = pat
		o.IsOverride = true
		byPattern[pat] = o
	}
	out := make([]domain.ModelPricing, 0, len(byPattern))
	for _, p := range byPattern {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsOverride != out[j].IsOverride {
			return out[i].IsOverride
		}
		return out[i].ModelPattern < out[j].ModelPattern
	})
	return out
}

// MergePlanBenchmarks merges user-configured benchmarks over default competitor baselines.
func MergePlanBenchmarks(custom []domain.PlanBenchmark) []domain.PlanBenchmark {
	byID := make(map[string]domain.PlanBenchmark, len(defaultPlanBenchmarks)+len(custom))
	for _, b := range defaultPlanBenchmarks {
		byID[b.ID] = finalizeBenchmark(b)
	}
	for _, c := range custom {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		c.ID = id
		c.IsCustom = true
		byID[id] = finalizeBenchmark(c)
	}
	out := make([]domain.PlanBenchmark, 0, len(byID))
	for _, b := range byID {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MonthlyCostUSD != out[j].MonthlyCostUSD {
			return out[i].MonthlyCostUSD < out[j].MonthlyCostUSD
		}
		return out[i].PlanName < out[j].PlanName
	})
	return out
}

// normalizePricingModelID strips "models/" and reasoning/tier suffixes so variants share base pricing.
func normalizePricingModelID(modelID string) string {
	norm := strings.ToLower(strings.TrimSpace(modelID))
	norm = strings.TrimPrefix(norm, "models/")
	suffixes := []string{"-tiered", "-thinking", "-xhigh", "-high", "-medium", "-med", "-low", "-minimal"}
	for changed := true; changed; {
		changed = false
		for _, suf := range suffixes {
			if strings.HasSuffix(norm, suf) {
				norm = strings.TrimSuffix(norm, suf)
				changed = true
			}
		}
	}
	return norm
}

// ResolveModelPricing matches a model identifier against overrides first, then built-in defaults.
func ResolveModelPricing(modelID string, catalog []domain.ModelPricing) (domain.ModelPricing, bool) {
	norm := normalizePricingModelID(modelID)
	if norm == "" {
		return domain.ModelPricing{}, false
	}

	var bestOverride *domain.ModelPricing
	var bestDefault *domain.ModelPricing

	for i := range catalog {
		p := &catalog[i]
		pat := strings.ToLower(strings.TrimSpace(p.ModelPattern))
		if pat == "" {
			continue
		}
		matched := norm == pat || strings.HasPrefix(norm, pat) || strings.Contains(norm, pat)
		if !matched {
			continue
		}
		if p.IsOverride {
			if bestOverride == nil || len(pat) > len(bestOverride.ModelPattern) || (norm == pat && bestOverride.ModelPattern != pat) {
				bestOverride = p
			}
		} else {
			if bestDefault == nil || len(pat) > len(bestDefault.ModelPattern) || (norm == pat && bestDefault.ModelPattern != pat) {
				bestDefault = p
			}
		}
	}

	if bestOverride != nil {
		return *bestOverride, true
	}
	if bestDefault != nil {
		return *bestDefault, true
	}
	return domain.ModelPricing{}, false
}

// MetricCostBreakdown holds double-count-safe itemized token quantities and API-equivalent USD cost.
type MetricCostBreakdown struct {
	UncachedInputTokens int64
	CachedInputTokens   int64
	CacheWriteTokens    int64
	OutputTokens        int64
	ThoughtsTokens      int64
	BillableOutput      int64
	APIValueUSD         float64
}

// ComputeMetricCost computes itemized token counts and API-equivalent USD cost for one TokenMetric.
func ComputeMetricCost(m *domain.TokenMetric, p domain.ModelPricing, priced bool) MetricCostBreakdown {
	if m == nil {
		return MetricCostBreakdown{}
	}
	cachedIn := m.CachedContentTokens
	if m.PromptTokens > 0 && cachedIn > m.PromptTokens {
		cachedIn = m.PromptTokens
	}
	uncachedIn := m.PromptTokens - cachedIn
	if uncachedIn < 0 {
		uncachedIn = 0
	}
	uncachedIn += m.ToolUsePromptTokens
	cacheWrite := m.CacheCreationTokens

	// Guard against double-counting thoughts when upstream already includes thoughts inside candidates_tokens.
	billableOut := m.CandidatesTokens
	if m.ThoughtsTokens > 0 {
		if m.TotalTokens == 0 || m.TotalTokens >= m.PromptTokens+m.CandidatesTokens+m.ThoughtsTokens {
			billableOut += m.ThoughtsTokens
		}
	}

	var usd float64
	if priced {
		inMult := 1.0
		outMult := 1.0
		totalContextIn := m.PromptTokens + m.ToolUsePromptTokens + m.CacheCreationTokens
		if p.LongContextThreshold > 0 && totalContextIn > p.LongContextThreshold {
			if p.LongContextInputMultiplier > 0 {
				inMult = p.LongContextInputMultiplier
			}
			if p.LongContextOutputMultiplier > 0 {
				outMult = p.LongContextOutputMultiplier
			}
		}
		usd = (float64(uncachedIn)*p.InputPerMillion*inMult +
			float64(cachedIn)*p.CachedInputPerMillion*inMult +
			float64(cacheWrite)*p.CacheWritePerMillion*inMult +
			float64(billableOut)*p.OutputPerMillion*outMult) / 1_000_000.0
	}

	return MetricCostBreakdown{
		UncachedInputTokens: uncachedIn,
		CachedInputTokens:   cachedIn,
		CacheWriteTokens:    cacheWrite,
		OutputTokens:        m.CandidatesTokens,
		ThoughtsTokens:      m.ThoughtsTokens,
		BillableOutput:      billableOut,
		APIValueUSD:         usd,
	}
}
