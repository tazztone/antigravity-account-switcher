package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

// MetricsRepository implements domain.MetricsRepository backed by SQLite.
type MetricsRepository struct {
	db *DB
}

// NewMetricsRepository creates a new SQLite MetricsRepository.
func NewMetricsRepository(db *DB) *MetricsRepository {
	return &MetricsRepository{db: db}
}

// Record logs an individual token usage event into token_metrics.
func (r *MetricsRepository) Record(ctx context.Context, m *domain.TokenMetric) error {
	now := time.Now().UTC()
	if m.Timestamp.IsZero() {
		m.Timestamp = now
	}
	if m.TotalTokens == 0 {
		m.TotalTokens = m.PromptTokens + m.CandidatesTokens
	}

	query := `
		INSERT INTO token_metrics (
			account_id, request_path, model, reasoning_mode, conversation_id,
			prompt_tokens, candidates_tokens, total_tokens, cached_content_tokens, thoughts_tokens,
			tool_use_prompt_tokens, cache_creation_tokens, timestamp
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	res, err := r.db.ExecContext(ctx, query,
		m.AccountID,
		m.RequestPath,
		m.Model,
		m.ReasoningMode,
		m.ConversationID,
		m.PromptTokens,
		m.CandidatesTokens,
		m.TotalTokens,
		m.CachedContentTokens,
		m.ThoughtsTokens,
		m.ToolUsePromptTokens,
		m.CacheCreationTokens,
		m.Timestamp.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("failed to record token metric: %w", err)
	}

	id, err := res.LastInsertId()
	if err == nil {
		m.ID = id
	}

	return nil
}

// Compile-time interface check.
var (
	_ domain.ConversationMetricsRepository = (*MetricsRepository)(nil)
	_ domain.TokenMetricQueryRepository    = (*MetricsRepository)(nil)
)

// ListMetricsSince retrieves raw token_metrics rows optionally filtered by accountID and start time.
func (r *MetricsRepository) ListMetricsSince(ctx context.Context, accountID string, since time.Time) ([]*domain.TokenMetric, error) {
	sinceStr := ""
	if !since.IsZero() {
		sinceStr = since.UTC().Format(time.RFC3339)
	}
	query := `
		SELECT id, account_id, request_path, model, reasoning_mode, conversation_id,
		       prompt_tokens, candidates_tokens, total_tokens, cached_content_tokens,
		       thoughts_tokens, tool_use_prompt_tokens, cache_creation_tokens, timestamp
		FROM token_metrics
		WHERE (? = '' OR account_id = ?)
		  AND (? = '' OR timestamp >= ?)
		ORDER BY timestamp ASC, id ASC
	`
	rows, err := r.db.QueryContext(ctx, query, accountID, accountID, sinceStr, sinceStr)
	if err != nil {
		return nil, fmt.Errorf("failed to list token metrics: %w", err)
	}
	defer rows.Close()

	var out []*domain.TokenMetric
	for rows.Next() {
		var m domain.TokenMetric
		var tsStr string
		if err := rows.Scan(
			&m.ID,
			&m.AccountID,
			&m.RequestPath,
			&m.Model,
			&m.ReasoningMode,
			&m.ConversationID,
			&m.PromptTokens,
			&m.CandidatesTokens,
			&m.TotalTokens,
			&m.CachedContentTokens,
			&m.ThoughtsTokens,
			&m.ToolUsePromptTokens,
			&m.CacheCreationTokens,
			&tsStr,
		); err != nil {
			return nil, fmt.Errorf("failed to scan token metric: %w", err)
		}
		m.Timestamp, _ = parseDBTime(tsStr)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// GetConversationSummaries returns aggregated token usage per conversation, ordered by last_seen DESC.
func (r *MetricsRepository) GetConversationSummaries(ctx context.Context, limit int) ([]domain.ConversationSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	query := `
		SELECT
			agg.conversation_id,
			COALESCE((
				SELECT t2.model FROM token_metrics t2
				WHERE t2.conversation_id = agg.conversation_id AND t2.model != ''
				ORDER BY t2.timestamp DESC, t2.id DESC LIMIT 1
			), '') AS latest_model,
			COALESCE((
				SELECT t3.reasoning_mode FROM token_metrics t3
				WHERE t3.conversation_id = agg.conversation_id AND t3.reasoning_mode != ''
				ORDER BY t3.timestamp DESC, t3.id DESC LIMIT 1
			), '') AS latest_reasoning_mode,
			agg.request_count,
			agg.prompt_tokens,
			agg.candidates_tokens,
			agg.total_tokens,
			agg.cached_content_tokens,
			agg.thoughts_tokens,
			agg.first_seen,
			agg.last_seen
		FROM (
			SELECT
				conversation_id,
				COUNT(*) AS request_count,
				COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
				COALESCE(SUM(candidates_tokens), 0) AS candidates_tokens,
				COALESCE(SUM(total_tokens), 0) AS total_tokens,
				COALESCE(SUM(cached_content_tokens), 0) AS cached_content_tokens,
				COALESCE(SUM(thoughts_tokens), 0) AS thoughts_tokens,
				MIN(timestamp) AS first_seen,
				MAX(timestamp) AS last_seen
			FROM token_metrics
			WHERE conversation_id != ''
			GROUP BY conversation_id
		) agg
		ORDER BY agg.last_seen DESC
		LIMIT ?
	`

	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query conversation summaries: %w", err)
	}
	defer rows.Close()

	out := make([]domain.ConversationSummary, 0)
	for rows.Next() {
		var s domain.ConversationSummary
		var firstStr, lastStr string
		if err := rows.Scan(
			&s.ConversationID,
			&s.LatestModel,
			&s.LatestReasoningMode,
			&s.RequestCount,
			&s.PromptTokens,
			&s.CandidatesTokens,
			&s.TotalTokens,
			&s.CachedContentTokens,
			&s.ThoughtsTokens,
			&firstStr,
			&lastStr,
		); err != nil {
			return nil, fmt.Errorf("failed to scan conversation summary: %w", err)
		}
		if t, parseErr := parseDBTime(firstStr); parseErr == nil {
			s.FirstSeen = t
		}
		if t, parseErr := parseDBTime(lastStr); parseErr == nil {
			s.LastSeen = t
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating conversation summaries: %w", err)
	}
	return out, nil
}

// GetSummary calculates aggregated usage for an account (or global pool if accountID is empty).
func (r *MetricsRepository) GetSummary(ctx context.Context, accountID string, period string) (*domain.AggregatedMetrics, error) {
	now := time.Now().UTC()
	var since time.Time

	switch strings.ToLower(strings.TrimSpace(period)) {
	case "day", "today", "daily":
		since = now.Add(-24 * time.Hour)
	case "week", "weekly":
		since = now.Add(-7 * 24 * time.Hour)
	case "month", "monthly":
		since = now.Add(-30 * 24 * time.Hour)
	case "lifetime", "total", "all", "":
		// No time cutoff
	default:
		return nil, fmt.Errorf("unknown summary period: %q", period)
	}

	sinceStr := ""
	if !since.IsZero() {
		sinceStr = since.Format(time.RFC3339)
	}

	query := `
		SELECT
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(candidates_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(cached_content_tokens), 0),
			COALESCE(SUM(thoughts_tokens), 0),
			COUNT(*)
		FROM token_metrics
		WHERE (? = '' OR account_id = ?)
		  AND (? = '' OR timestamp >= ?)
	`

	var agg domain.AggregatedMetrics
	err := r.db.QueryRowContext(ctx, query, accountID, accountID, sinceStr, sinceStr).Scan(
		&agg.TotalPromptTokens,
		&agg.TotalCandidatesTokens,
		&agg.TotalTokens,
		&agg.TotalCachedContentTokens,
		&agg.TotalThoughtsTokens,
		&agg.TotalRequests,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate token metrics: %w", err)
	}

	return &agg, nil
}

// GetAccountSummaries aggregates token metrics grouped by account_id for a given period.
func (r *MetricsRepository) GetAccountSummaries(ctx context.Context, period string) (map[string]*domain.AggregatedMetrics, error) {
	now := time.Now().UTC()
	var since time.Time

	switch strings.ToLower(strings.TrimSpace(period)) {
	case "day", "today", "daily":
		since = now.Add(-24 * time.Hour)
	case "week", "weekly":
		since = now.Add(-7 * 24 * time.Hour)
	case "month", "monthly":
		since = now.Add(-30 * 24 * time.Hour)
	case "lifetime", "total", "all", "":
		// No time cutoff
	default:
		return nil, fmt.Errorf("unknown summary period: %q", period)
	}

	sinceStr := ""
	if !since.IsZero() {
		sinceStr = since.Format(time.RFC3339)
	}

	query := `
		SELECT
			account_id,
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(candidates_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(cached_content_tokens), 0),
			COALESCE(SUM(thoughts_tokens), 0),
			COUNT(*)
		FROM token_metrics
		WHERE (? = '' OR timestamp >= ?)
		GROUP BY account_id
	`

	rows, err := r.db.QueryContext(ctx, query, sinceStr, sinceStr)
	if err != nil {
		return nil, fmt.Errorf("failed to query account token summaries: %w", err)
	}
	defer rows.Close()

	summaries := make(map[string]*domain.AggregatedMetrics)
	for rows.Next() {
		var accountID string
		var agg domain.AggregatedMetrics
		err := rows.Scan(
			&accountID,
			&agg.TotalPromptTokens,
			&agg.TotalCandidatesTokens,
			&agg.TotalTokens,
			&agg.TotalCachedContentTokens,
			&agg.TotalThoughtsTokens,
			&agg.TotalRequests,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan account summary row: %w", err)
		}
		summaries[accountID] = &agg
	}

	return summaries, rows.Err()
}

// GetDailyHistory retrieves daily aggregated token usage for chart rendering over the past N days in UTC.
func (r *MetricsRepository) GetDailyHistory(ctx context.Context, accountID string, days int) ([]*domain.DailyTokenUsage, error) {
	return r.GetDailyHistoryInLocation(ctx, accountID, days, time.UTC)
}

// GetDailyHistoryInLocation retrieves daily aggregated token usage for chart rendering over the past N days in a specified location.
func (r *MetricsRepository) GetDailyHistoryInLocation(ctx context.Context, accountID string, days int, loc *time.Location) ([]*domain.DailyTokenUsage, error) {
	if days <= 0 {
		days = 14
	}
	if loc == nil {
		loc = time.UTC
	}

	now := time.Now().In(loc)
	earliestDay := now.AddDate(0, 0, -(days - 1))
	startOfWindow := time.Date(earliestDay.Year(), earliestDay.Month(), earliestDay.Day(), 0, 0, 0, 0, loc)
	sinceStr := startOfWindow.UTC().Format(time.RFC3339)

	query := `
		SELECT
			strftime('%Y-%m-%dT%H:00:00Z', timestamp) AS hour_bucket,
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(candidates_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(cached_content_tokens), 0),
			COALESCE(SUM(thoughts_tokens), 0),
			COUNT(*)
		FROM token_metrics
		WHERE (? = '' OR account_id = ?)
		  AND timestamp >= ?
		GROUP BY hour_bucket
		ORDER BY hour_bucket ASC
	`

	rows, err := r.db.QueryContext(ctx, query, accountID, accountID, sinceStr)
	if err != nil {
		return nil, fmt.Errorf("failed to query daily token history: %w", err)
	}
	defer rows.Close()

	byDay := make(map[string]*domain.DailyTokenUsage)
	var dayOrder []string
	for rows.Next() {
		var hourStr string
		var prompt, candidates, total, cached, thoughts, count int64
		if err := rows.Scan(&hourStr, &prompt, &candidates, &total, &cached, &thoughts, &count); err != nil {
			return nil, fmt.Errorf("failed to scan daily token history: %w", err)
		}
		t, err := time.Parse(time.RFC3339, hourStr)
		if err != nil {
			continue
		}
		dayKey := t.In(loc).Format("2006-01-02")
		u, exists := byDay[dayKey]
		if !exists {
			u = &domain.DailyTokenUsage{Date: dayKey}
			byDay[dayKey] = u
			dayOrder = append(dayOrder, dayKey)
		}
		u.PromptTokens += prompt
		u.CandidatesTokens += candidates
		u.TotalTokens += total
		u.CachedContentTokens += cached
		u.ThoughtsTokens += thoughts
		u.RequestCount += count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	history := make([]*domain.DailyTokenUsage, 0, len(dayOrder))
	for _, dayKey := range dayOrder {
		history = append(history, byDay[dayKey])
	}
	return history, nil
}
