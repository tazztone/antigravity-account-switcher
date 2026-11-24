package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

// ValueRepository implements domain.ValueAnalyticsRepository backed by SQLite.
type ValueRepository struct {
	db *DB
}

// NewValueRepository creates a new SQLite ValueRepository.
func NewValueRepository(db *DB) *ValueRepository {
	return &ValueRepository{db: db}
}

var _ domain.ValueAnalyticsRepository = (*ValueRepository)(nil)

// ListPricingOverrides returns all user-configured model pricing overrides.
func (r *ValueRepository) ListPricingOverrides(ctx context.Context) ([]domain.ModelPricing, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT model_pattern, display_name, input_per_million, cached_input_per_million,
		       cache_write_per_million, output_per_million
		FROM model_pricing_overrides
		ORDER BY model_pattern ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list pricing overrides: %w", err)
	}
	defer rows.Close()

	var out []domain.ModelPricing
	for rows.Next() {
		var p domain.ModelPricing
		if err := rows.Scan(
			&p.ModelPattern,
			&p.DisplayName,
			&p.InputPerMillion,
			&p.CachedInputPerMillion,
			&p.CacheWritePerMillion,
			&p.OutputPerMillion,
		); err != nil {
			return nil, fmt.Errorf("failed to scan pricing override: %w", err)
		}
		p.IsOverride = true
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertPricingOverride inserts or updates a model pricing override.
func (r *ValueRepository) UpsertPricingOverride(ctx context.Context, p domain.ModelPricing) error {
	pattern := strings.ToLower(strings.TrimSpace(p.ModelPattern))
	if pattern == "" {
		return fmt.Errorf("model_pattern cannot be empty")
	}
	nowStr := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO model_pricing_overrides (
			model_pattern, display_name, input_per_million, cached_input_per_million,
			cache_write_per_million, output_per_million, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model_pattern) DO UPDATE SET
			display_name = excluded.display_name,
			input_per_million = excluded.input_per_million,
			cached_input_per_million = excluded.cached_input_per_million,
			cache_write_per_million = excluded.cache_write_per_million,
			output_per_million = excluded.output_per_million,
			updated_at = excluded.updated_at
	`, pattern, p.DisplayName, p.InputPerMillion, p.CachedInputPerMillion, p.CacheWritePerMillion, p.OutputPerMillion, nowStr)
	if err != nil {
		return fmt.Errorf("failed to upsert pricing override: %w", err)
	}
	return nil
}

// DeletePricingOverride deletes a pricing override by model_pattern.
func (r *ValueRepository) DeletePricingOverride(ctx context.Context, pattern string) error {
	clean := strings.ToLower(strings.TrimSpace(pattern))
	if clean == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM model_pricing_overrides WHERE model_pattern = ?`, clean)
	if err != nil {
		return fmt.Errorf("failed to delete pricing override: %w", err)
	}
	return nil
}

// ListPlanBenchmarks returns all stored custom/overridden competitor plan benchmarks.
func (r *ValueRepository) ListPlanBenchmarks(ctx context.Context) ([]domain.PlanBenchmark, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, provider, plan_name, monthly_cost_usd, window_5h_api_usd, weekly_api_usd, notes
		FROM plan_benchmarks
		ORDER BY monthly_cost_usd ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list plan benchmarks: %w", err)
	}
	defer rows.Close()

	var out []domain.PlanBenchmark
	for rows.Next() {
		var b domain.PlanBenchmark
		if err := rows.Scan(
			&b.ID,
			&b.Provider,
			&b.PlanName,
			&b.MonthlyCostUSD,
			&b.Window5hAPIUSD,
			&b.WeeklyAPIUSD,
			&b.Notes,
		); err != nil {
			return nil, fmt.Errorf("failed to scan plan benchmark: %w", err)
		}
		b.IsCustom = true
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpsertPlanBenchmark inserts or updates a competitor plan benchmark.
func (r *ValueRepository) UpsertPlanBenchmark(ctx context.Context, b domain.PlanBenchmark) error {
	id := strings.TrimSpace(b.ID)
	if id == "" {
		return fmt.Errorf("benchmark id cannot be empty")
	}
	nowStr := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO plan_benchmarks (
			id, provider, plan_name, monthly_cost_usd, window_5h_api_usd, weekly_api_usd, notes, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			provider = excluded.provider,
			plan_name = excluded.plan_name,
			monthly_cost_usd = excluded.monthly_cost_usd,
			window_5h_api_usd = excluded.window_5h_api_usd,
			weekly_api_usd = excluded.weekly_api_usd,
			notes = excluded.notes,
			updated_at = excluded.updated_at
	`, id, b.Provider, b.PlanName, b.MonthlyCostUSD, b.Window5hAPIUSD, b.WeeklyAPIUSD, b.Notes, nowStr)
	if err != nil {
		return fmt.Errorf("failed to upsert plan benchmark: %w", err)
	}
	return nil
}

// DeletePlanBenchmark removes a custom/overridden benchmark by id.
func (r *ValueRepository) DeletePlanBenchmark(ctx context.Context, id string) error {
	clean := strings.TrimSpace(id)
	if clean == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM plan_benchmarks WHERE id = ?`, clean)
	if err != nil {
		return fmt.Errorf("failed to delete plan benchmark: %w", err)
	}
	return nil
}
