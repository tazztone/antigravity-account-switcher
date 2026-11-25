package domain

import (
	"context"
	"strings"
	"time"
)

// QuotaWindow represents the sliding limit window (daily or weekly).
type QuotaWindow string

const (
	// QuotaWindowDaily denotes daily recurring quota limits.
	QuotaWindowDaily QuotaWindow = "DAILY"
	// QuotaWindowWeekly denotes weekly recurring quota limits.
	QuotaWindowWeekly QuotaWindow = "WEEKLY"
)

// QuotaBucket models a specific model/feature quota limit bucket for an account.
type QuotaBucket struct {
	AccountID         string      `json:"account_id"`
	BucketID          string      `json:"bucket_id"`
	DisplayName       string      `json:"display_name"`
	Window            QuotaWindow `json:"window"`
	RemainingFraction float64     `json:"remaining_fraction"` // 0.0 (exhausted) to 1.0 (full)
	RemainingAmount   int64       `json:"remaining_amount"`   // Available requests or token units
	ResetTime         time.Time   `json:"reset_time"`         // Point in time when quota refreshes
	Source            string      `json:"source,omitempty"`   // "cloud_code", "language_server", "synthetic", "429_exhaustion"
	UpdatedAt         time.Time   `json:"updated_at"`
}

// QuotaSnapshot records a historical quota observation for burn-rate calibration.
type QuotaSnapshot struct {
	ID                int64     `json:"id"`
	AccountID         string    `json:"account_id"`
	BucketID          string    `json:"bucket_id"`
	DisplayName       string    `json:"display_name"`
	Category          string    `json:"category"`
	Window            string    `json:"window"`
	RemainingFraction float64   `json:"remaining_fraction"`
	RemainingAmount   int64     `json:"remaining_amount"`
	ResetTime         time.Time `json:"reset_time"`
	Source            string    `json:"source"`
	CapturedAt        time.Time `json:"captured_at"`
}

// CategorizeBucket maps a quota bucket ID and display name to its canonical ModelCategory.
func CategorizeBucket(bucketID, displayName string) ModelCategory {
	idLower := strings.ToLower(bucketID)
	nameLower := strings.ToLower(displayName)
	if strings.Contains(idLower, "claude") || strings.Contains(idLower, "gpt") || strings.Contains(idLower, "3p") ||
		strings.Contains(nameLower, "claude") || strings.Contains(nameLower, "gpt") || strings.Contains(nameLower, "3p") {
		return CategoryClaudeGPT
	}
	if strings.Contains(idLower, "gemini") || strings.Contains(nameLower, "gemini") {
		return CategoryGemini
	}
	return CategoryUnknown
}

// NormalizeQuotaWindow maps API and language_server quota window strings to canonical "5h" or "weekly".
func NormalizeQuotaWindow(window, bucketID string) string {
	wLower := strings.ToLower(window)
	idLower := strings.ToLower(bucketID)
	if strings.Contains(wLower, "weekly") || strings.Contains(idLower, "weekly") {
		return "weekly"
	}
	return "5h"
}

// QuotaSnapshotRepository persists and queries append-only quota snapshots.
type QuotaSnapshotRepository interface {
	RecordSnapshotsIfChanged(ctx context.Context, snapshots []*QuotaSnapshot) (int, error)
	ListSnapshots(ctx context.Context, accountID string, since time.Time) ([]*QuotaSnapshot, error)
	ListLatestSnapshotsBefore(ctx context.Context, accountID string, before time.Time) ([]*QuotaSnapshot, error)
}

// IsExhausted returns true if remaining quota fraction is zero or negligible.
func (q *QuotaBucket) IsExhausted() bool {
	return q.RemainingFraction <= 0.0 || (q.RemainingAmount == 0 && q.RemainingFraction < 0.05)
}

// HasReset determines if the quota reset time has passed relative to now.
func (q *QuotaBucket) HasReset(now time.Time) bool {
	return !q.ResetTime.IsZero() && now.After(q.ResetTime)
}

// QuotaRepository defines persistence operations for QuotaBucket entities.
type QuotaRepository interface {
	// UpsertBuckets inserts or updates multiple quota buckets atomically.
	UpsertBuckets(ctx context.Context, buckets []*QuotaBucket) error

	// GetByAccountID retrieves all quota buckets associated with an account.
	GetByAccountID(ctx context.Context, accountID string) ([]*QuotaBucket, error)

	// ListAll returns a mapping of accountID -> list of QuotaBuckets.
	ListAll(ctx context.Context) (map[string][]*QuotaBucket, error)

	// DeleteByAccountID removes all quota buckets for a specific account.
	DeleteByAccountID(ctx context.Context, accountID string) error

	// GetExhaustedAccountsPastReset returns IDs of accounts marked 'exhausted'
	// whose buckets have all passed their reset time.
	GetExhaustedAccountsPastReset(ctx context.Context, now time.Time) ([]string, error)
}
