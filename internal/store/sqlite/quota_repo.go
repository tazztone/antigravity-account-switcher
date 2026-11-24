package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

// QuotaRepository implements domain.QuotaRepository backed by SQLite.
type QuotaRepository struct {
	db *DB
}

// NewQuotaRepository creates a new SQLite QuotaRepository.
func NewQuotaRepository(db *DB) *QuotaRepository {
	return &QuotaRepository{db: db}
}

// UpsertBuckets inserts or updates quota buckets atomically.
func (r *QuotaRepository) UpsertBuckets(ctx context.Context, buckets []*domain.QuotaBucket) error {
	if len(buckets) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `
		INSERT INTO quota_buckets (
			account_id, bucket_id, display_name, window,
			remaining_fraction, remaining_amount, reset_time, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, bucket_id) DO UPDATE SET
			display_name = excluded.display_name,
			window = excluded.window,
			remaining_fraction = excluded.remaining_fraction,
			remaining_amount = excluded.remaining_amount,
			reset_time = excluded.reset_time,
			updated_at = excluded.updated_at
	`
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to prepare upsert statement: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	for _, b := range buckets {
		updatedAt := b.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = now
		}
		resetStr := ""
		if !b.ResetTime.IsZero() {
			resetStr = b.ResetTime.UTC().Format(time.RFC3339)
		}

		_, err := stmt.ExecContext(ctx,
			b.AccountID,
			b.BucketID,
			b.DisplayName,
			string(b.Window),
			b.RemainingFraction,
			b.RemainingAmount,
			resetStr,
			updatedAt.UTC().Format(time.RFC3339),
		)
		if err != nil {
			return fmt.Errorf("failed to upsert bucket %s/%s: %w", b.AccountID, b.BucketID, err)
		}
	}

	return tx.Commit()
}

// GetByAccountID retrieves all quota buckets for a specific account.
func (r *QuotaRepository) GetByAccountID(ctx context.Context, accountID string) ([]*domain.QuotaBucket, error) {
	query := `
		SELECT account_id, bucket_id, display_name, window, remaining_fraction, remaining_amount, reset_time, updated_at
		FROM quota_buckets
		WHERE account_id = ?
		ORDER BY bucket_id ASC
	`
	rows, err := r.db.QueryContext(ctx, query, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to query quota buckets: %w", err)
	}
	defer rows.Close()

	var buckets []*domain.QuotaBucket
	for rows.Next() {
		b, err := r.scanBucket(rows)
		if err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}

	return buckets, rows.Err()
}

// ListAll retrieves all quota buckets across all accounts grouped by account ID.
func (r *QuotaRepository) ListAll(ctx context.Context) (map[string][]*domain.QuotaBucket, error) {
	query := `
		SELECT account_id, bucket_id, display_name, window, remaining_fraction, remaining_amount, reset_time, updated_at
		FROM quota_buckets
		ORDER BY account_id ASC, bucket_id ASC
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list all quota buckets: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]*domain.QuotaBucket)
	for rows.Next() {
		b, err := r.scanBucket(rows)
		if err != nil {
			return nil, err
		}
		result[b.AccountID] = append(result[b.AccountID], b)
	}

	return result, rows.Err()
}

// DeleteByAccountID removes all quota buckets associated with an account.
func (r *QuotaRepository) DeleteByAccountID(ctx context.Context, accountID string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM quota_buckets WHERE account_id = ?", accountID)
	if err != nil {
		return fmt.Errorf("failed to delete quota buckets: %w", err)
	}
	return nil
}

// ReplaceBucketsForAccount atomically deletes existing buckets for accountID and inserts the new buckets.
func (r *QuotaRepository) ReplaceBucketsForAccount(ctx context.Context, accountID string, buckets []*domain.QuotaBucket) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM quota_buckets WHERE account_id = ?`, accountID); err != nil {
		return fmt.Errorf("failed to delete quota buckets: %w", err)
	}

	if len(buckets) > 0 {
		query := `
			INSERT INTO quota_buckets (account_id, bucket_id, display_name, window, remaining_fraction, remaining_amount, reset_time, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(account_id, bucket_id) DO UPDATE SET
				display_name = excluded.display_name,
				window = excluded.window,
				remaining_fraction = excluded.remaining_fraction,
				remaining_amount = excluded.remaining_amount,
				reset_time = excluded.reset_time,
				updated_at = excluded.updated_at
		`
		stmt, err := tx.PrepareContext(ctx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare upsert statement: %w", err)
		}
		defer stmt.Close()

		now := time.Now().UTC()
		for _, b := range buckets {
			updatedAt := b.UpdatedAt
			if updatedAt.IsZero() {
				updatedAt = now
			}
			resetStr := ""
			if !b.ResetTime.IsZero() {
				resetStr = b.ResetTime.UTC().Format(time.RFC3339)
			}
			if _, err := stmt.ExecContext(ctx,
				b.AccountID,
				b.BucketID,
				b.DisplayName,
				string(b.Window),
				b.RemainingFraction,
				b.RemainingAmount,
				resetStr,
				updatedAt.UTC().Format(time.RFC3339),
			); err != nil {
				return fmt.Errorf("failed to upsert bucket %s/%s: %w", b.AccountID, b.BucketID, err)
			}
		}
	}

	return tx.Commit()
}

// GetExhaustedAccountsPastReset returns account IDs marked 'exhausted' where all exhausted quota buckets
// have passed their reset time.
func (r *QuotaRepository) GetExhaustedAccountsPastReset(ctx context.Context, now time.Time) ([]string, error) {
	nowStr := now.UTC().Format(time.RFC3339)
	query := `
		SELECT a.id
		FROM accounts a
		WHERE a.status = 'exhausted'
		  AND EXISTS (SELECT 1 FROM quota_buckets qb WHERE qb.account_id = a.id)
		  AND NOT EXISTS (
			  SELECT 1 FROM quota_buckets qb
			  WHERE qb.account_id = a.id
			    AND qb.remaining_fraction <= 0.0
			    AND (qb.reset_time = '' OR qb.reset_time = '0001-01-01T00:00:00Z' OR qb.reset_time > ?)
		  )
		ORDER BY a.updated_at ASC
	`
	rows, err := r.db.QueryContext(ctx, query, nowStr)
	if err != nil {
		return nil, fmt.Errorf("failed to query reset accounts: %w", err)
	}
	defer rows.Close()

	var accountIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan reset account id: %w", err)
		}
		accountIDs = append(accountIDs, id)
	}

	return accountIDs, rows.Err()
}

func (r *QuotaRepository) scanBucket(scanner rowScanner) (*domain.QuotaBucket, error) {
	var b domain.QuotaBucket
	var windowStr, resetStr, updatedStr string

	err := scanner.Scan(
		&b.AccountID,
		&b.BucketID,
		&b.DisplayName,
		&windowStr,
		&b.RemainingFraction,
		&b.RemainingAmount,
		&resetStr,
		&updatedStr,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan quota bucket: %w", err)
	}

	b.Window = domain.QuotaWindow(windowStr)
	b.ResetTime, _ = parseDBTime(resetStr)
	b.UpdatedAt, _ = parseDBTime(updatedStr)

	return &b, nil
}

// Compile-time check for QuotaSnapshotRepository.
var _ domain.QuotaSnapshotRepository = (*QuotaRepository)(nil)

// RecordSnapshotsIfChanged appends quota snapshots for any bucket whose remaining_fraction,
// reset_time (>2m shift), or 429_exhaustion source changed relative to its latest stored snapshot.
func (r *QuotaRepository) RecordSnapshotsIfChanged(ctx context.Context, snapshots []*domain.QuotaSnapshot) (int, error) {
	if len(snapshots) == 0 {
		return 0, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin snapshot tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	latestStmt, err := tx.PrepareContext(ctx, `
		SELECT remaining_fraction, reset_time, source
		FROM quota_snapshots
		WHERE account_id = ? AND bucket_id = ?
		ORDER BY captured_at DESC, id DESC
		LIMIT 1
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to prepare latest snapshot query: %w", err)
	}
	defer latestStmt.Close()

	insertStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO quota_snapshots (
			account_id, bucket_id, display_name, category, window,
			remaining_fraction, remaining_amount, reset_time, source, captured_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to prepare insert snapshot statement: %w", err)
	}
	defer insertStmt.Close()

	inserted := 0
	now := time.Now().UTC()
	for _, s := range snapshots {
		if s == nil || s.AccountID == "" || s.BucketID == "" {
			continue
		}
		capturedAt := s.CapturedAt
		if capturedAt.IsZero() {
			capturedAt = now
		}
		category := s.Category
		if category == "" {
			category = string(domain.CategorizeBucket(s.BucketID, s.DisplayName))
		}
		source := s.Source
		if source == "" {
			source = "cloud_code"
		}

		var prevFrac float64
		var prevResetStr, prevSource string
		err := latestStmt.QueryRowContext(ctx, s.AccountID, s.BucketID).Scan(&prevFrac, &prevResetStr, &prevSource)
		shouldInsert := false
		if err == sql.ErrNoRows {
			shouldInsert = true
		} else if err != nil {
			return 0, fmt.Errorf("failed to check latest snapshot: %w", err)
		} else {
			if math.Abs(s.RemainingFraction-prevFrac) >= 1e-6 {
				shouldInsert = true
			} else if (source == "429_exhaustion") != (prevSource == "429_exhaustion") {
				shouldInsert = true
			} else {
				prevReset, _ := parseDBTime(prevResetStr)
				if resetTimeShifted(prevReset, s.ResetTime) {
					shouldInsert = true
				}
			}
		}

		if !shouldInsert {
			continue
		}

		resetStr := ""
		if !s.ResetTime.IsZero() {
			resetStr = s.ResetTime.UTC().Format(time.RFC3339)
		}
		res, insErr := insertStmt.ExecContext(ctx,
			s.AccountID,
			s.BucketID,
			s.DisplayName,
			category,
			s.Window,
			s.RemainingFraction,
			s.RemainingAmount,
			resetStr,
			source,
			capturedAt.UTC().Format(time.RFC3339),
		)
		if insErr != nil {
			return 0, fmt.Errorf("failed to insert snapshot %s/%s: %w", s.AccountID, s.BucketID, insErr)
		}
		if id, idErr := res.LastInsertId(); idErr == nil {
			s.ID = id
		}
		inserted++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit snapshots: %w", err)
	}
	return inserted, nil
}

func resetTimeShifted(a, b time.Time) bool {
	if a.IsZero() && b.IsZero() {
		return false
	}
	if a.IsZero() != b.IsZero() {
		return true
	}
	diff := a.Sub(b)
	if diff < 0 {
		diff = -diff
	}
	return diff > 2*time.Minute
}

// ListSnapshots retrieves recorded quota snapshots optionally filtered by accountID and start time.
func (r *QuotaRepository) ListSnapshots(ctx context.Context, accountID string, since time.Time) ([]*domain.QuotaSnapshot, error) {
	sinceStr := ""
	if !since.IsZero() {
		sinceStr = since.UTC().Format(time.RFC3339)
	}
	query := `
		SELECT id, account_id, bucket_id, display_name, category, window,
		       remaining_fraction, remaining_amount, reset_time, source, captured_at
		FROM quota_snapshots
		WHERE (? = '' OR account_id = ?)
		  AND (? = '' OR captured_at >= ?)
		ORDER BY account_id ASC, bucket_id ASC, captured_at ASC, id ASC
	`
	rows, err := r.db.QueryContext(ctx, query, accountID, accountID, sinceStr, sinceStr)
	if err != nil {
		return nil, fmt.Errorf("failed to list quota snapshots: %w", err)
	}
	defer rows.Close()

	var out []*domain.QuotaSnapshot
	for rows.Next() {
		var s domain.QuotaSnapshot
		var resetStr, capturedStr string
		if err := rows.Scan(
			&s.ID,
			&s.AccountID,
			&s.BucketID,
			&s.DisplayName,
			&s.Category,
			&s.Window,
			&s.RemainingFraction,
			&s.RemainingAmount,
			&resetStr,
			&s.Source,
			&capturedStr,
		); err != nil {
			return nil, fmt.Errorf("failed to scan quota snapshot: %w", err)
		}
		s.ResetTime, _ = parseDBTime(resetStr)
		s.CapturedAt, _ = parseDBTime(capturedStr)
		out = append(out, &s)
	}
	return out, rows.Err()
}
