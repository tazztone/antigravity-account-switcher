package web

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

func (a *APIHandler) valueService() domain.ValueAnalyticsService {
	if a.metricsService == nil {
		return nil
	}
	svc, ok := a.metricsService.(domain.ValueAnalyticsService)
	if !ok {
		return nil
	}
	return svc
}

func (a *APIHandler) updateAccountPlan(w http.ResponseWriter, r *http.Request, accountID string) {
	if a.accountRepo == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "account repository unavailable", nil)
		return
	}
	planRepo, ok := a.accountRepo.(domain.AccountPlanRepository)
	if !ok {
		writeErrorJSON(w, http.StatusNotImplemented, "account plan updates not supported", nil)
		return
	}

	var req struct {
		PlanTier       string   `json:"plan_tier"`
		MonthlyCostUSD *float64 `json:"monthly_cost_usd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, "invalid request payload", err)
		return
	}

	tier := strings.ToLower(strings.TrimSpace(req.PlanTier))
	if tier == "" {
		tier = "ai_pro"
	}
	cost := 20.0
	switch tier {
	case "free":
		cost = 0.0
	case "ai_ultra":
		cost = 250.0
	case "ai_pro":
		cost = 20.0
	}
	if req.MonthlyCostUSD != nil {
		if *req.MonthlyCostUSD < 0 {
			writeErrorJSON(w, http.StatusBadRequest, "monthly_cost_usd cannot be negative", nil)
			return
		}
		cost = *req.MonthlyCostUSD
	}

	if err := planRepo.UpdatePlan(r.Context(), accountID, tier, cost); err != nil {
		if errors.Is(err, domain.ErrAccountNotFound) {
			writeErrorJSON(w, http.StatusNotFound, "account not found", err)
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, "failed to update account plan", err)
		return
	}

	acc, err := a.accountRepo.GetByID(r.Context(), accountID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":               true,
			"account_id":       accountID,
			"plan_tier":        tier,
			"monthly_cost_usd": cost,
		})
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

// HandleAnalyticsValue serves GET /api/analytics/value.
func (a *APIHandler) HandleAnalyticsValue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	svc := a.valueService()
	if svc == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "value analytics service unavailable", nil)
		return
	}

	period := domain.MetricPeriod(strings.TrimSpace(r.URL.Query().Get("period")))
	if period == "" {
		period = domain.PeriodWeek
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))

	report, err := svc.GetPlanValueReport(r.Context(), period, accountID)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidPeriod) {
			writeErrorJSON(w, http.StatusBadRequest, "invalid period", err)
			return
		}
		if errors.Is(err, domain.ErrAccountNotFound) {
			writeErrorJSON(w, http.StatusNotFound, "account not found", err)
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, "failed to compute plan value report", err)
		return
	}

	writeJSON(w, http.StatusOK, report)
}

// HandleAnalyticsExport serves GET /api/analytics/export?period=...&format=json|csv.
func (a *APIHandler) HandleAnalyticsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	svc := a.valueService()
	if svc == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "value analytics service unavailable", nil)
		return
	}

	period := domain.MetricPeriod(strings.TrimSpace(r.URL.Query().Get("period")))
	if period == "" {
		period = domain.PeriodWeek
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}

	report, err := svc.GetPlanValueReport(r.Context(), period, accountID)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidPeriod) {
			writeErrorJSON(w, http.StatusBadRequest, "invalid period", err)
			return
		}
		writeErrorJSON(w, http.StatusInternalServerError, "failed to export plan value report", err)
		return
	}

	filenameSlug := string(report.Period)
	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="antigravity-plan-value-%s.json"`, filenameSlug))
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="antigravity-plan-value-%s.csv"`, filenameSlug))
		w.WriteHeader(http.StatusOK)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{
			"record_type", "account_id", "email", "plan_tier", "category", "window_or_model",
			"start_time", "end_time", "reset_time", "start_fraction", "end_fraction", "delta_fraction",
			"requests", "uncached_input_tokens", "cached_input_tokens", "output_tokens", "thoughts_tokens",
			"api_value_usd", "implied_100pct_usd", "notes",
		})
		for _, ep := range report.Episodes {
			resetStr := ""
			if !ep.ResetTime.IsZero() {
				resetStr = ep.ResetTime.UTC().Format(time.RFC3339)
			}
			_ = cw.Write([]string{
				"episode",
				ep.AccountID,
				ep.Email,
				ep.PlanTier,
				ep.Category,
				ep.Window,
				ep.StartTime.UTC().Format(time.RFC3339),
				ep.EndTime.UTC().Format(time.RFC3339),
				resetStr,
				strconv.FormatFloat(ep.StartFraction, 'f', 4, 64),
				strconv.FormatFloat(ep.EndFraction, 'f', 4, 64),
				strconv.FormatFloat(ep.DeltaFraction, 'f', 4, 64),
				strconv.FormatInt(ep.Requests, 10),
				strconv.FormatInt(ep.UncachedInputTokens, 10),
				strconv.FormatInt(ep.CachedInputTokens, 10),
				strconv.FormatInt(ep.OutputTokens, 10),
				strconv.FormatInt(ep.ThoughtsTokens, 10),
				strconv.FormatFloat(ep.APIValueUSD, 'f', 6, 64),
				strconv.FormatFloat(ep.Implied100PctUSD, 'f', 4, 64),
				ep.SourceStart + "->" + ep.SourceEnd,
			})
		}
		for _, m := range report.ByModel {
			_ = cw.Write([]string{
				"model_summary",
				"",
				"",
				"",
				m.Category,
				m.Model,
				"",
				"",
				"",
				"",
				"",
				"",
				strconv.FormatInt(m.Requests, 10),
				strconv.FormatInt(m.UncachedInputTokens, 10),
				strconv.FormatInt(m.CachedInputTokens, 10),
				strconv.FormatInt(m.OutputTokens, 10),
				strconv.FormatInt(m.ThoughtsTokens, 10),
				strconv.FormatFloat(m.APIValueUSD, 'f', 6, 64),
				"",
				m.PricingPattern,
			})
		}
		cw.Flush()
	default:
		writeErrorJSON(w, http.StatusBadRequest, "unsupported export format (use json or csv)", nil)
	}
}

// HandleAnalyticsPricing serves GET, PUT, and DELETE /api/analytics/pricing.
func (a *APIHandler) HandleAnalyticsPricing(w http.ResponseWriter, r *http.Request) {
	svc := a.valueService()
	if svc == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "value analytics service unavailable", nil)
		return
	}

	switch r.Method {
	case http.MethodGet:
		catalog, err := svc.ListPricingCatalog(r.Context())
		if err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "failed to load pricing catalog", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"pricing": catalog})
	case http.MethodPut:
		var p domain.ModelPricing
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErrorJSON(w, http.StatusBadRequest, "invalid pricing payload", err)
			return
		}
		if strings.TrimSpace(p.ModelPattern) == "" {
			writeErrorJSON(w, http.StatusBadRequest, "model_pattern is required", nil)
			return
		}
		if p.InputPerMillion < 0 || p.CachedInputPerMillion < 0 || p.CacheWritePerMillion < 0 || p.OutputPerMillion < 0 {
			writeErrorJSON(w, http.StatusBadRequest, "pricing rates cannot be negative", nil)
			return
		}
		if err := svc.UpsertPricingOverride(r.Context(), p); err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "failed to save pricing override", err)
			return
		}
		catalog, err := svc.ListPricingCatalog(r.Context())
		if err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "failed to reload pricing catalog", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"pricing": catalog})
	case http.MethodDelete:
		pattern := strings.TrimSpace(r.URL.Query().Get("model_pattern"))
		if pattern == "" {
			writeErrorJSON(w, http.StatusBadRequest, "model_pattern query param is required", nil)
			return
		}
		if err := svc.DeletePricingOverride(r.Context(), pattern); err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "failed to delete pricing override", err)
			return
		}
		catalog, err := svc.ListPricingCatalog(r.Context())
		if err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "failed to reload pricing catalog", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"pricing": catalog})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleAnalyticsBenchmarks serves GET, PUT, and DELETE /api/analytics/benchmarks.
func (a *APIHandler) HandleAnalyticsBenchmarks(w http.ResponseWriter, r *http.Request) {
	svc := a.valueService()
	if svc == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable, "value analytics service unavailable", nil)
		return
	}

	switch r.Method {
	case http.MethodGet:
		benchmarks, err := svc.ListPlanBenchmarks(r.Context())
		if err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "failed to load benchmarks", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"benchmarks": benchmarks})
	case http.MethodPut:
		var b domain.PlanBenchmark
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			writeErrorJSON(w, http.StatusBadRequest, "invalid benchmark payload", err)
			return
		}
		if strings.TrimSpace(b.ID) == "" || strings.TrimSpace(b.PlanName) == "" {
			writeErrorJSON(w, http.StatusBadRequest, "id and plan_name are required", nil)
			return
		}
		if b.MonthlyCostUSD < 0 || b.Window5hAPIUSD < 0 || b.WeeklyAPIUSD < 0 {
			writeErrorJSON(w, http.StatusBadRequest, "benchmark USD values cannot be negative", nil)
			return
		}
		if err := svc.UpsertPlanBenchmark(r.Context(), b); err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "failed to save benchmark", err)
			return
		}
		benchmarks, err := svc.ListPlanBenchmarks(r.Context())
		if err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "failed to reload benchmarks", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"benchmarks": benchmarks})
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			writeErrorJSON(w, http.StatusBadRequest, "id query param is required", nil)
			return
		}
		if err := svc.DeletePlanBenchmark(r.Context(), id); err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "failed to delete benchmark", err)
			return
		}
		benchmarks, err := svc.ListPlanBenchmarks(r.Context())
		if err != nil {
			writeErrorJSON(w, http.StatusInternalServerError, "failed to reload benchmarks", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"benchmarks": benchmarks})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
