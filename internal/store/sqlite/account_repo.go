package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/google/uuid"
)

// AccountRepository implements domain.AccountRepository backed by SQLite.
type AccountRepository struct {
	db *DB
}

// bucketCategoryPredicate matches quota_buckets rows (alias qb) belonging to a
// model category: Claude/GPT third-party buckets for "claude_gpt", Gemini 4 Argon
// weekly buckets for "gemini_argon", and regular Gemini buckets (explicitly not
// Argon) for "gemini". Two placeholders are bound to the category string.
const bucketCategoryPredicate = `
	AND (
		(? = 'claude_gpt' AND (
			LOWER(qb.bucket_id) LIKE '%claude%' OR LOWER(qb.bucket_id) LIKE '%gpt%' OR LOWER(qb.bucket_id) LIKE '%3p%' OR
			LOWER(qb.display_name) LIKE '%claude%' OR LOWER(qb.display_name) LIKE '%gpt%' OR LOWER(qb.display_name) LIKE '%3p%'
		))
		OR
		(? = 'gemini_argon' AND (
			LOWER(qb.bucket_id) LIKE '%argon%' OR LOWER(qb.display_name) LIKE '%argon%'
		))
		OR
		(? = 'gemini' AND (
			(LOWER(qb.bucket_id) LIKE '%gemini%' OR LOWER(qb.display_name) LIKE '%gemini%')
			AND LOWER(qb.bucket_id) NOT LIKE '%argon%' AND LOWER(qb.display_name) NOT LIKE '%argon%'
		))
	)
`

// argonEntitlementPredicate requires accounts matching category "gemini_argon"
// to have Argon entitlement, evidenced by either an Argon quota bucket or an
// enabled Argon model rule. For any other category, the condition is a no-op.
const argonEntitlementPredicate = `
	AND (
		? != 'gemini_argon'
		OR EXISTS (
			SELECT 1 FROM quota_buckets qb
			WHERE qb.account_id = a.id
			  AND (LOWER(qb.bucket_id) LIKE '%argon%' OR LOWER(qb.display_name) LIKE '%argon%')
		)
		OR EXISTS (
			SELECT 1 FROM account_model_rules mr
			WHERE mr.account_id = a.id
			  AND (
			      (mr.category = 'gemini_argon' AND LOWER(mr.model_id) NOT LIKE '%disabled%')
			      OR (LOWER(mr.model_id) LIKE '%argon%' AND LOWER(mr.model_id) NOT LIKE '%disabled%')
			      OR (mr.category = 'claude_gpt' AND (LOWER(mr.model_id) LIKE '%5-5%' OR LOWER(mr.model_id) LIKE '%5.5%'))
			  )
			  AND mr.enabled = 1
		)
	)
`

// NewAccountRepository creates a new SQLite AccountRepository.
func NewAccountRepository(db *DB) *AccountRepository {
	return &AccountRepository{db: db}
}

// Create inserts a new account record into SQLite.
func (r *AccountRepository) Create(ctx context.Context, acc *domain.Account) error {
	if acc.ID == "" {
		acc.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if acc.CreatedAt.IsZero() {
		acc.CreatedAt = now
	}
	if acc.UpdatedAt.IsZero() {
		acc.UpdatedAt = now
	}
	if acc.Status == "" {
		acc.Status = domain.AccountStatusActive
	}

	expiryStr := acc.TokenExpiry.Format(time.RFC3339)
	if acc.TokenExpiry.IsZero() {
		expiryStr = "1970-01-01T00:00:00Z"
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	query := `
		INSERT INTO accounts (
			id, email, refresh_token, access_token, token_expiry,
			is_active, status, created_at, updated_at, disabled_categories
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	disabledCats := strings.Join(acc.DisabledCategories, ",")
	_, err = tx.ExecContext(ctx, query,
		acc.ID,
		acc.Email,
		acc.RefreshToken,
		acc.AccessToken,
		expiryStr,
		acc.IsActive,
		string(acc.Status),
		acc.CreatedAt.Format(time.RFC3339),
		acc.UpdatedAt.Format(time.RFC3339),
		disabledCats,
	)
	if err != nil {
		if strings.Contains(err.Error(), "accounts.email") && strings.Contains(err.Error(), "UNIQUE") {
			return domain.ErrAccountEmailExists
		}
		return fmt.Errorf("failed to create account: %w", err)
	}

	if acc.IsActive {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO active_category_accounts (category, account_id)
			VALUES ('claude_gpt', ?), ('gemini', ?), ('gemini_argon', ?)
			ON CONFLICT(category) DO UPDATE SET account_id = excluded.account_id
		`, acc.ID, acc.ID, acc.ID); err != nil {
			return fmt.Errorf("failed to seed active_category_accounts on create: %w", err)
		}
	}

	return tx.Commit()
}

// GetByID retrieves an account by ID.
func (r *AccountRepository) GetByID(ctx context.Context, id string) (*domain.Account, error) {
	query := `
		SELECT id, email, refresh_token, access_token, token_expiry, is_active, status, created_at, updated_at, disabled_categories
		FROM accounts
		WHERE id = ?
	`
	row := r.db.QueryRowContext(ctx, query, id)
	return r.scanAccount(row)
}

// GetByEmail retrieves an account by email.
func (r *AccountRepository) GetByEmail(ctx context.Context, email string) (*domain.Account, error) {
	query := `
		SELECT id, email, refresh_token, access_token, token_expiry, is_active, status, created_at, updated_at, disabled_categories
		FROM accounts
		WHERE email = ?
	`
	row := r.db.QueryRowContext(ctx, query, email)
	return r.scanAccount(row)
}

// GetActive retrieves the single currently active account.
func (r *AccountRepository) GetActive(ctx context.Context) (*domain.Account, error) {
	query := `
		SELECT id, email, refresh_token, access_token, token_expiry, is_active, status, created_at, updated_at, disabled_categories
		FROM accounts
		WHERE is_active = 1
		LIMIT 1
	`
	row := r.db.QueryRowContext(ctx, query)
	acc, err := r.scanAccount(row)
	if errors.Is(err, domain.ErrAccountNotFound) {
		return nil, domain.ErrNoActiveAccount
	}
	return acc, err
}

// List returns all accounts in the pool ordered by creation date.
func (r *AccountRepository) List(ctx context.Context) ([]*domain.Account, error) {
	query := `
		SELECT id, email, refresh_token, access_token, token_expiry, is_active, status, created_at, updated_at, disabled_categories
		FROM accounts
		ORDER BY created_at ASC
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}
	defer rows.Close()

	var accounts []*domain.Account
	for rows.Next() {
		acc, err := r.scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, acc)
	}

	return accounts, rows.Err()
}

// SetActive atomically sets the designated account active and clears active status from all others.
func (r *AccountRepository) SetActive(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	nowStr := time.Now().UTC().Format(time.RFC3339)

	// Step 1: Deactivate currently active account
	if _, err := tx.ExecContext(ctx, "UPDATE accounts SET is_active = 0, updated_at = ? WHERE is_active = 1", nowStr); err != nil {
		return fmt.Errorf("failed to deactivate current active account: %w", err)
	}

	// Step 2: Activate target account and mark status as active
	res, err := tx.ExecContext(ctx, "UPDATE accounts SET is_active = 1, status = 'active', updated_at = ? WHERE id = ?", nowStr, id)
	if err != nil {
		return fmt.Errorf("failed to activate target account %s: %w", id, err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrAccountNotFound
	}

	// Step 3: Populate active_category_accounts for every model category
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO active_category_accounts (category, account_id)
		VALUES ('claude_gpt', ?), ('gemini', ?), ('gemini_argon', ?)
		ON CONFLICT(category) DO UPDATE SET account_id = excluded.account_id
	`, id, id, id); err != nil {
		return fmt.Errorf("failed to update active_category_accounts: %w", err)
	}

	return tx.Commit()
}

// UpdateStatus modifies the operational status of an account.
func (r *AccountRepository) UpdateStatus(ctx context.Context, id string, status domain.AccountStatus) error {
	nowStr := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, "UPDATE accounts SET status = ?, updated_at = ? WHERE id = ?", string(status), nowStr, id)
	if err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrAccountNotFound
	}

	return nil
}

// UpdateToken updates the access token and expiry time for an account.
func (r *AccountRepository) UpdateToken(ctx context.Context, id string, accessToken string, expiry time.Time) error {
	nowStr := time.Now().UTC().Format(time.RFC3339)
	expiryStr := expiry.Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
		UPDATE accounts
		SET access_token = ?, token_expiry = ?, updated_at = ?
		WHERE id = ?
	`, accessToken, expiryStr, nowStr, id)
	if err != nil {
		return fmt.Errorf("failed to update token: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrAccountNotFound
	}

	return nil
}

// UpdateRefreshToken updates the stored OAuth2 refresh token for an account.
func (r *AccountRepository) UpdateRefreshToken(ctx context.Context, id string, refreshToken string) error {
	nowStr := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, "UPDATE accounts SET refresh_token = ?, updated_at = ? WHERE id = ?", refreshToken, nowStr, id)
	if err != nil {
		return fmt.Errorf("failed to update refresh token: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrAccountNotFound
	}

	return nil
}

// Delete removes an account and cascades deletions to related buckets and metrics.
func (r *AccountRepository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete account: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrAccountNotFound
	}

	return nil
}

// GetNextAvailable returns the least-recently updated account in 'active' status,
// excluding the given account ID (for failover rotation).
func (r *AccountRepository) GetNextAvailable(ctx context.Context, excludeID string) (*domain.Account, error) {
	query := `
		SELECT id, email, refresh_token, access_token, token_expiry, is_active, status, created_at, updated_at, disabled_categories
		FROM accounts
		WHERE status = 'active'
		  AND (? = '' OR id != ?)
		ORDER BY updated_at ASC
		LIMIT 1
	`
	row := r.db.QueryRowContext(ctx, query, excludeID, excludeID)
	acc, err := r.scanAccount(row)
	if errors.Is(err, domain.ErrAccountNotFound) {
		return nil, domain.ErrNoAvailableAccount
	}
	return acc, err
}

// GetActiveForCategory retrieves the active account designated for a specific model category.
// If the pinned account's bucket for that category is exhausted (and not yet past
// its reset time), or if the account is ineligible/unentitled for that category,
// the pin is advanced to the next eligible account so traffic does not keep hitting
// an unusable account; when no candidate exists the current pin is returned unchanged.
func (r *AccountRepository) GetActiveForCategory(ctx context.Context, category domain.ModelCategory) (*domain.Account, error) {
	catStr := category.String()
	if catStr == "unknown" || catStr == "" {
		return r.GetActive(ctx)
	}

	query := `
		SELECT a.id, a.email, a.refresh_token, a.access_token, a.token_expiry, a.is_active, a.status, a.created_at, a.updated_at, a.disabled_categories
		FROM accounts a
		JOIN active_category_accounts aca ON a.id = aca.account_id
		WHERE aca.category = ?
		LIMIT 1
	`
	row := r.db.QueryRowContext(ctx, query, catStr)
	acc, err := r.scanAccount(row)
	if err == nil {
		if acc.IsCategoryDisabled(category) {
			mode, _ := r.GetCategoryRoutingMode(ctx, category)
			if mode != domain.RoutingModeSolo {
				next, nErr := r.GetNextAvailableForCategory(ctx, category, acc.ID)
				if nErr == nil && next != nil {
					_ = r.SetActiveForCategory(ctx, category, next.ID)
					return next, nil
				}
			}
			return nil, domain.ErrNoActiveAccount
		}

		mode, _ := r.GetCategoryRoutingMode(ctx, category)
		if mode == domain.RoutingModeSolo {
			return acc, nil
		}
		if category == domain.CategoryGeminiArgon {
			entitled, eErr := r.HasArgonEntitlement(ctx, acc.ID)
			if eErr == nil && !entitled {
				next, nErr := r.GetNextAvailableForCategory(ctx, category, acc.ID)
				if nErr == nil && next != nil {
					_ = r.SetActiveForCategory(ctx, category, next.ID)
					return next, nil
				}
				return nil, domain.ErrNoActiveAccount
			}
		}
		dead, dErr := r.hasExhaustedBucketForCategory(ctx, acc.ID, catStr)
		if dErr != nil || !dead {
			return acc, nil
		}
		next, nErr := r.GetNextAvailableForCategory(ctx, category, acc.ID)
		if nErr != nil || next == nil {
			return acc, nil
		}
		if sErr := r.SetActiveForCategory(ctx, category, next.ID); sErr != nil {
			return acc, nil
		}
		return next, nil
	}

	if errors.Is(err, domain.ErrAccountNotFound) {
		cand, cErr := r.GetNextAvailableForCategory(ctx, category, "")
		if cErr == nil && cand != nil {
			_, _ = r.db.ExecContext(ctx, `
				INSERT OR IGNORE INTO active_category_accounts (category, account_id)
				VALUES (?, ?)
			`, catStr, cand.ID)
			return cand, nil
		}
		if category != domain.CategoryGeminiArgon {
			globalActive, gErr := r.GetActive(ctx)
			if gErr == nil && globalActive != nil {
				_, _ = r.db.ExecContext(ctx, `
					INSERT OR IGNORE INTO active_category_accounts (category, account_id)
					VALUES (?, ?)
				`, catStr, globalActive.ID)
				return globalActive, nil
			}
		}
		return nil, domain.ErrNoActiveAccount
	}

	return nil, err
}

// hasExhaustedBucketForCategory reports whether the account has an exhausted
// quota bucket for the given category whose reset time is still in the future.
func (r *AccountRepository) hasExhaustedBucketForCategory(ctx context.Context, accountID, catStr string) (bool, error) {
	nowStr := time.Now().UTC().Format(time.RFC3339)
	query := `
		SELECT COUNT(1) FROM quota_buckets qb
		WHERE qb.account_id = ?
		  AND qb.remaining_fraction <= 0.0
		  AND qb.reset_time > ?
	` + bucketCategoryPredicate
	var count int
	if err := r.db.QueryRowContext(ctx, query, accountID, nowStr, catStr, catStr, catStr).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// SetActiveForCategory sets the active account pointer for a specific model category.
// It only touches active_category_accounts: accounts.is_active remains the global
// primary IDE login managed solely by SetActive, so per-category failover never
// misattributes the local IDE quota buckets polled via acc.IsActive.
func (r *AccountRepository) SetActiveForCategory(ctx context.Context, category domain.ModelCategory, accountID string) error {
	if accountID == "" {
		return domain.ErrAccountNotFound
	}
	catStr := category.String()
	if catStr == "unknown" || catStr == "" {
		return r.SetActive(ctx, accountID)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Verify target account exists
	var count int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE id = ?", accountID).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to verify account %s: %w", accountID, err)
	}
	if count == 0 {
		return domain.ErrAccountNotFound
	}

	// 2. Upsert into active_category_accounts
	upsertSQL := `
		INSERT INTO active_category_accounts (category, account_id)
		VALUES (?, ?)
		ON CONFLICT(category) DO UPDATE SET account_id = excluded.account_id
	`
	if _, err := tx.ExecContext(ctx, upsertSQL, catStr, accountID); err != nil {
		return fmt.Errorf("failed to set active account for category %s: %w", catStr, err)
	}

	return tx.Commit()
}

// GetNextAvailableForCategory selects the next eligible account for a model category,
// excluding the specified account ID and accounts whose quota for that category is exhausted.
//
// Eligibility rules:
//   - Accounts with the category in disabled_categories are excluded.
//   - 'active' accounts are eligible unless a recorded bucket for the category
//     is exhausted and not yet past reset.
//   - 'exhausted' accounts are eligible when quota rows positively show a
//     healthy or reset-elapsed bucket for this category (remaining_fraction > 0.0
//     or reset_time <= now): a 429 in one category must not lock the account
//     out of the other, and an account whose reset window has passed recovers
//     immediately without waiting for the next background poller cycle.
//     A bare 'exhausted' status without quota evidence keeps the account excluded.
//   - 'error' and 'disabled' accounts are never eligible.
func (r *AccountRepository) GetNextAvailableForCategory(ctx context.Context, category domain.ModelCategory, excludeID string) (*domain.Account, error) {
	catStr := category.String()
	if catStr == "unknown" || catStr == "" {
		return r.GetNextAvailable(ctx, excludeID)
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	query := `
		SELECT a.id, a.email, a.refresh_token, a.access_token, a.token_expiry, a.is_active, a.status, a.created_at, a.updated_at, a.disabled_categories
		FROM accounts a
		WHERE (? = '' OR a.id != ?)
		  AND (',' || a.disabled_categories || ',') NOT LIKE '%,' || ? || ',%'
		  AND (
			a.status = 'active'
			OR (
				a.status = 'exhausted'
				AND EXISTS (
					SELECT 1 FROM quota_buckets qb
					WHERE qb.account_id = a.id
					  AND (
						qb.remaining_fraction > 0.0
						OR (qb.reset_time != '' AND qb.reset_time <= ?)
					  )
	` + bucketCategoryPredicate + `
				)
			)
		  )
		  AND NOT EXISTS (
			  SELECT 1 FROM quota_buckets qb
			  WHERE qb.account_id = a.id
			    AND qb.remaining_fraction <= 0.0
			    AND qb.reset_time > ?
	` + bucketCategoryPredicate + `
		  )
	` + argonEntitlementPredicate + `
		ORDER BY a.updated_at ASC
		LIMIT 1
	`
	row := r.db.QueryRowContext(ctx, query, excludeID, excludeID, catStr, nowStr, catStr, catStr, catStr, nowStr, catStr, catStr, catStr, catStr)
	acc, err := r.scanAccount(row)
	if errors.Is(err, domain.ErrAccountNotFound) {
		return nil, domain.ErrNoAvailableAccount
	}
	return acc, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (r *AccountRepository) scanAccount(scanner rowScanner) (*domain.Account, error) {
	var acc domain.Account
	var statusStr, expiryStr, createdStr, updatedStr, disabledCatsStr string
	var isActiveInt int

	err := scanner.Scan(
		&acc.ID,
		&acc.Email,
		&acc.RefreshToken,
		&acc.AccessToken,
		&expiryStr,
		&isActiveInt,
		&statusStr,
		&createdStr,
		&updatedStr,
		&disabledCatsStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrAccountNotFound
		}
		return nil, fmt.Errorf("failed to scan account: %w", err)
	}

	acc.IsActive = isActiveInt == 1
	acc.Status = domain.AccountStatus(statusStr)
	acc.TokenExpiry, _ = parseDBTime(expiryStr)
	acc.CreatedAt, _ = parseDBTime(createdStr)
	acc.UpdatedAt, _ = parseDBTime(updatedStr)
	acc.DisabledCategories = []string{}
	if strings.TrimSpace(disabledCatsStr) != "" {
		for _, part := range strings.Split(disabledCatsStr, ",") {
			p := strings.TrimSpace(part)
			if p != "" {
				acc.DisabledCategories = append(acc.DisabledCategories, p)
			}
		}
	}

	return &acc, nil
}

// GetActiveCategoriesByAccount returns the pinned model categories per account ID
// by inverting the active_category_accounts table.
func (r *AccountRepository) GetActiveCategoriesByAccount(ctx context.Context) (map[string][]domain.ModelCategory, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT account_id, category FROM active_category_accounts")
	if err != nil {
		return nil, fmt.Errorf("failed to list active category accounts: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]domain.ModelCategory)
	for rows.Next() {
		var accountID, category string
		if err := rows.Scan(&accountID, &category); err != nil {
			return nil, fmt.Errorf("failed to scan active category account: %w", err)
		}
		result[accountID] = append(result[accountID], domain.ModelCategory(category))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate active category accounts: %w", err)
	}
	return result, nil
}

// UpsertAccountModels persists a discovered model catalog for an account.
// New models are inserted enabled; existing rows keep their enabled/fallback overrides.
func (r *AccountRepository) UpsertAccountModels(ctx context.Context, accountID string, models []*domain.ModelInfo) error {
	if accountID == "" || len(models) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	nowStr := time.Now().UTC().Format(time.RFC3339)
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO account_model_rules (account_id, model_id, display_name, category, enabled, fallback_account_id, updated_at, discovered)
		VALUES (?, ?, ?, ?, 1, '', ?, 1)
		ON CONFLICT(account_id, model_id) DO UPDATE SET
			display_name = excluded.display_name,
			category = excluded.category,
			updated_at = excluded.updated_at,
			discovered = 1
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare upsert: %w", err)
	}
	defer stmt.Close()

	freshIDs := make([]string, 0, len(models))
	for _, m := range models {
		if m == nil || m.ID == "" {
			continue
		}
		displayName := m.DisplayName
		if displayName == "" {
			displayName = m.ID
		}
		category := m.Category
		if category == "" {
			category = domain.CategoryUnknown.String()
		}
		if _, err := stmt.ExecContext(ctx, accountID, m.ID, displayName, category, nowStr); err != nil {
			return fmt.Errorf("failed to upsert model rule %s: %w", m.ID, err)
		}
		freshIDs = append(freshIDs, m.ID)
	}

	// Self-heal: any model previously marked discovered for this account that is absent
	// from the freshly discovered catalog must have discovered reset to 0, so stale or
	// polluted rules no longer trick IsAccountEligibleForModel. Explicit user toggles
	// (enabled = 0) are preserved.
	if len(freshIDs) > 0 {
		placeholders := make([]string, len(freshIDs))
		args := make([]any, 0, len(freshIDs)+3)
		args = append(args, nowStr, accountID)
		for i, id := range freshIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		pruneQuery := `
			UPDATE account_model_rules
			SET discovered = 0, updated_at = ?
			WHERE account_id = ? AND discovered = 1
			  AND model_id NOT IN (` + strings.Join(placeholders, ",") + `)
		`
		if _, err := tx.ExecContext(ctx, pruneQuery, args...); err != nil {
			return fmt.Errorf("failed to reset stale discovered model rules: %w", err)
		}
	}

	return tx.Commit()
}

// ListAccountModelRules returns per-model eligibility rules for an account.
func (r *AccountRepository) ListAccountModelRules(ctx context.Context, accountID string) ([]*domain.AccountModelRule, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT account_id, model_id, display_name, category, enabled, updated_at
		FROM account_model_rules
		WHERE account_id = ?
		ORDER BY model_id ASC
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list account model rules: %w", err)
	}
	defer rows.Close()

	var result []*domain.AccountModelRule
	for rows.Next() {
		var rule domain.AccountModelRule
		var enabledInt int
		var updatedStr string
		if err := rows.Scan(&rule.AccountID, &rule.ModelID, &rule.DisplayName, &rule.Category, &enabledInt, &updatedStr); err != nil {
			return nil, fmt.Errorf("failed to scan account model rule: %w", err)
		}
		rule.Enabled = enabledInt == 1
		rule.UpdatedAt, _ = parseDBTime(updatedStr)
		result = append(result, &rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate account model rules: %w", err)
	}
	return result, nil
}

// UpdateAccountModelRule upserts the enabled flag for one
// model on an account. Missing rows are inserted with the provided values.
func (r *AccountRepository) UpdateAccountModelRule(ctx context.Context, rule *domain.AccountModelRule) error {
	if rule == nil || rule.AccountID == "" || rule.ModelID == "" {
		return domain.ErrAccountNotFound
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE id = ?", rule.AccountID).Scan(&count); err != nil {
		return fmt.Errorf("failed to verify account %s: %w", rule.AccountID, err)
	}
	if count == 0 {
		return domain.ErrAccountNotFound
	}

	enabledInt := 0
	if rule.Enabled {
		enabledInt = 1
	}
	nowStr := time.Now().UTC().Format(time.RFC3339)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO account_model_rules (account_id, model_id, display_name, category, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, model_id) DO UPDATE SET
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, rule.AccountID, rule.ModelID, rule.DisplayName, rule.Category, enabledInt, nowStr)
	if err != nil {
		return fmt.Errorf("failed to update model rule %s: %w", rule.ModelID, err)
	}

	return tx.Commit()
}

// IsAccountEligibleForModel reports whether an account may serve a model.
// Fail-open: an account with no discovered model catalog is eligible for everything,
// unless an explicit rule with enabled = 0 exists for the model. Once a catalog is
// discovered (discovered = 1), only discovered and enabled models are served.
// The lookup runs as a single statement because it sits on the per-request proxy dispatch path.
func (r *AccountRepository) IsAccountEligibleForModel(ctx context.Context, accountID, modelID string) (bool, error) {
	if accountID == "" {
		return false, domain.ErrAccountNotFound
	}
	if modelID == "" {
		return true, nil
	}

	lower := strings.ToLower(modelID)
	// Internal IDE completion / tab models are always eligible
	if strings.HasPrefix(lower, "tab_") || strings.HasPrefix(lower, "chat_") {
		return true, nil
	}

	query := `
		SELECT
			(SELECT COUNT(*) FROM account_model_rules WHERE account_id = ? AND discovered = 1),
			COALESCE((SELECT enabled FROM account_model_rules WHERE account_id = ? AND model_id = ? AND discovered = 1), -1),
			COALESCE((SELECT enabled FROM account_model_rules WHERE account_id = ? AND model_id = ?), -1)
	`
	var discoveredCount, discoveredEnabled, rawEnabled int
	if err := r.db.QueryRowContext(ctx, query, accountID, accountID, modelID, accountID, modelID).
		Scan(&discoveredCount, &discoveredEnabled, &rawEnabled); err != nil {
		return false, fmt.Errorf("failed to query model rule: %w", err)
	}

	// 1. Explicit user toggle takes absolute precedence
	if rawEnabled == 0 {
		return false, nil
	}

	// 2. Discovered catalog exists and model was discovered enabled
	if discoveredEnabled == 1 {
		return true, nil
	}

	// 3. No explicit rule for this model: internal or preview models bypass
	if domain.IsInternalOrPreviewModel(modelID) {
		return true, nil
	}

	// 4. No discovered catalog for this account: fail-open for unconfigured models
	if discoveredCount == 0 {
		return true, nil
	}

	// 5. Discovered catalog exists, but this model was never discovered on this account
	// (or was reset because it is not in the account's actual catalog)
	return false, nil
}

// GetNextAvailableForModel selects the next account eligible for a specific model:
// active status, not exhausted for the category, not excluded, and either without
// discovered catalog (and not explicitly disabled) or with the model present and enabled.
func (r *AccountRepository) GetNextAvailableForModel(ctx context.Context, category domain.ModelCategory, modelID, excludeID string) (*domain.Account, error) {
	if modelID == "" || domain.IsInternalOrPreviewModel(modelID) {
		return r.GetNextAvailableForCategory(ctx, category, excludeID)
	}

	// Base candidate query mirrors GetNextAvailableForCategory (category quota +
	// status), then adds the per-account model eligibility constraint.
	next, err := r.GetNextAvailableForCategory(ctx, category, excludeID)
	if err != nil || next == nil {
		return next, err
	}

	eligible, err := r.IsAccountEligibleForModel(ctx, next.ID, modelID)
	if err != nil {
		return nil, err
	}
	if eligible {
		return next, nil
	}

	// Candidate ineligible for this model: fall back to a model-aware SQL scan.
	catStr := category.String()
	nowStr := time.Now().UTC().Format(time.RFC3339)
	query := `
		SELECT a.id, a.email, a.refresh_token, a.access_token, a.token_expiry, a.is_active, a.status, a.created_at, a.updated_at, a.disabled_categories
		FROM accounts a
		WHERE (? = '' OR a.id != ?)
		  AND (',' || a.disabled_categories || ',') NOT LIKE '%,' || ? || ',%'
		  AND (
			a.status = 'active'
			OR (
				a.status = 'exhausted'
				AND EXISTS (
					SELECT 1 FROM quota_buckets qb
					WHERE qb.account_id = a.id
					  AND (
						qb.remaining_fraction > 0.0
						OR (qb.reset_time != '' AND qb.reset_time <= ?)
					  )
	` + bucketCategoryPredicate + `
				)
			)
		  )
		  AND NOT EXISTS (
			  SELECT 1 FROM quota_buckets qb
			  WHERE qb.account_id = a.id
			    AND qb.remaining_fraction <= 0.0
			    AND qb.reset_time > ?
	` + bucketCategoryPredicate + `
		  )
	` + argonEntitlementPredicate + `
		  AND (
			(
				NOT EXISTS (SELECT 1 FROM account_model_rules mr WHERE mr.account_id = a.id AND mr.discovered = 1)
				AND NOT EXISTS (SELECT 1 FROM account_model_rules mr WHERE mr.account_id = a.id AND mr.model_id = ? AND mr.enabled = 0)
			)
			OR EXISTS (
				SELECT 1 FROM account_model_rules mr
				WHERE mr.account_id = a.id AND mr.model_id = ? AND mr.enabled = 1
			)
		  )
		ORDER BY a.updated_at ASC
		LIMIT 1
	`
	args := []any{excludeID, excludeID, catStr, nowStr, catStr, catStr, catStr, nowStr, catStr, catStr, catStr, catStr, modelID, modelID}
	row := r.db.QueryRowContext(ctx, query, args...)
	acc, err := r.scanAccount(row)
	if errors.Is(err, domain.ErrAccountNotFound) {
		return nil, domain.ErrNoAvailableAccount
	}
	return acc, err
}

// HasArgonEntitlement reports whether the account has Argon entitlement, evidenced
// by an Argon quota bucket, an enabled Argon model rule, or Pro-tier model rules (e.g. Claude 5.5).
func (r *AccountRepository) HasArgonEntitlement(ctx context.Context, accountID string) (bool, error) {
	query := `
		SELECT 1 FROM quota_buckets qb
		WHERE qb.account_id = ?
		  AND (LOWER(qb.bucket_id) LIKE '%argon%' OR LOWER(qb.display_name) LIKE '%argon%')
		UNION
		SELECT 1 FROM account_model_rules mr
		WHERE mr.account_id = ?
		  AND (
		      (mr.category = 'gemini_argon' AND LOWER(mr.model_id) NOT LIKE '%disabled%')
		      OR (LOWER(mr.model_id) LIKE '%argon%' AND LOWER(mr.model_id) NOT LIKE '%disabled%')
		      OR (mr.category = 'claude_gpt' AND (LOWER(mr.model_id) LIKE '%5-5%' OR LOWER(mr.model_id) LIKE '%5.5%'))
		  )
		  AND mr.enabled = 1
		LIMIT 1
	`
	var exists int
	err := r.db.QueryRowContext(ctx, query, accountID, accountID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// IsAccountAvailableForCategory reports whether one specific account may serve
// traffic for a model category right now. It mirrors the eligibility rules of
// GetNextAvailableForCategory: 'active' accounts need no unexpired exhausted
// bucket for the category, 'exhausted' accounts need positive healthy bucket
// evidence for it, disabled_categories excludes the category, and 'error'/'disabled' accounts are never available.
func (r *AccountRepository) IsAccountAvailableForCategory(ctx context.Context, accountID string, category domain.ModelCategory) (bool, error) {
	if accountID == "" {
		return false, domain.ErrAccountNotFound
	}

	catStr := category.String()
	if catStr == "unknown" || catStr == "" {
		acc, err := r.GetByID(ctx, accountID)
		if err != nil {
			return false, err
		}
		return acc.IsAvailable(), nil
	}

	acc, err := r.GetByID(ctx, accountID)
	if err != nil {
		return false, err
	}
	if acc.IsCategoryDisabled(category) {
		return false, nil
	}

	if category == domain.CategoryGeminiArgon {
		entitled, err := r.HasArgonEntitlement(ctx, accountID)
		if err != nil {
			return false, err
		}
		if !entitled {
			return false, nil
		}
	}

	dead, err := r.hasExhaustedBucketForCategory(ctx, accountID, catStr)
	if err != nil {
		return false, err
	}
	if dead {
		return false, nil
	}

	switch acc.Status {
	case domain.AccountStatusActive:
		return true, nil
	case domain.AccountStatusExhausted:
		// Exhausted accounts stay usable for a category only when quota rows
		// positively show a healthy bucket for it (a 429 in one category must not
		// lock the account out of the others).
		query := `
			SELECT COUNT(1) FROM quota_buckets qb
			WHERE qb.account_id = ?
			  AND qb.remaining_fraction > 0.0
		` + bucketCategoryPredicate
		var healthy int
		if err := r.db.QueryRowContext(ctx, query, accountID, catStr, catStr, catStr).Scan(&healthy); err != nil {
			return false, fmt.Errorf("failed to inspect quota buckets for account %s: %w", accountID, err)
		}
		return healthy > 0, nil
	default:
		return false, nil
	}
}

// GetCategoryRoutingMode returns the routing mode ("pool" or "solo") for a model category.
func (r *AccountRepository) GetCategoryRoutingMode(ctx context.Context, category domain.ModelCategory) (string, error) {
	catStr := category.String()
	if catStr == "" || catStr == domain.CategoryUnknown.String() {
		return domain.RoutingModePool, nil
	}

	var mode string
	err := r.db.QueryRowContext(ctx, "SELECT routing_mode FROM active_category_accounts WHERE category = ?", catStr).Scan(&mode)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.RoutingModePool, nil
		}
		return domain.RoutingModePool, fmt.Errorf("failed to get category routing mode: %w", err)
	}
	if mode == "" {
		return domain.RoutingModePool, nil
	}
	return mode, nil
}

// SetCategoryRoutingMode sets the routing mode ("pool" or "solo") for a model category.
func (r *AccountRepository) SetCategoryRoutingMode(ctx context.Context, category domain.ModelCategory, mode string) error {
	catStr := category.String()
	if catStr == "" || catStr == domain.CategoryUnknown.String() {
		return nil
	}
	if mode != domain.RoutingModeSolo {
		mode = domain.RoutingModePool
	}

	res, err := r.db.ExecContext(ctx, "UPDATE active_category_accounts SET routing_mode = ? WHERE category = ?", mode, catStr)
	if err != nil {
		return fmt.Errorf("failed to update category routing mode for %s: %w", catStr, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		eligible, err := r.GetNextAvailableForCategory(ctx, category, "")
		if err != nil || eligible == nil {
			return domain.ErrNoAvailableAccount
		}
		_, err = r.db.ExecContext(ctx, `
			INSERT INTO active_category_accounts (category, account_id, routing_mode)
			VALUES (?, ?, ?)
			ON CONFLICT(category) DO UPDATE SET routing_mode = excluded.routing_mode
		`, catStr, eligible.ID, mode)
		if err != nil {
			return fmt.Errorf("failed to insert category routing mode for %s: %w", catStr, err)
		}
	}
	return nil
}

// GetAllCategoryRoutingModes returns a map of all categories to their routing mode.
func (r *AccountRepository) GetAllCategoryRoutingModes(ctx context.Context) (map[domain.ModelCategory]string, error) {
	modes := map[domain.ModelCategory]string{
		domain.CategoryClaudeGPT:   domain.RoutingModePool,
		domain.CategoryGemini:      domain.RoutingModePool,
		domain.CategoryGeminiArgon: domain.RoutingModePool,
	}

	rows, err := r.db.QueryContext(ctx, "SELECT category, routing_mode FROM active_category_accounts")
	if err != nil {
		return modes, fmt.Errorf("failed to list category routing modes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cat, mode string
		if err := rows.Scan(&cat, &mode); err != nil {
			return modes, fmt.Errorf("failed to scan category routing mode: %w", err)
		}
		if mode == "" {
			mode = domain.RoutingModePool
		}
		modes[domain.ModelCategory(cat)] = mode
	}
	return modes, rows.Err()
}

// SetAccountCategoryDisabled updates the exclusion state for a model category on an account.
func (r *AccountRepository) SetAccountCategoryDisabled(ctx context.Context, accountID string, category domain.ModelCategory, disabled bool) error {
	if accountID == "" {
		return domain.ErrAccountNotFound
	}
	catStr := category.String()
	if catStr == "" || catStr == domain.CategoryUnknown.String() {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentStr string
	err = tx.QueryRowContext(ctx, "SELECT disabled_categories FROM accounts WHERE id = ?", accountID).Scan(&currentStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrAccountNotFound
		}
		return fmt.Errorf("failed to query account %s: %w", accountID, err)
	}

	catMap := make(map[string]bool)
	for _, part := range strings.Split(currentStr, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			catMap[p] = true
		}
	}

	if disabled {
		catMap[catStr] = true
	} else {
		delete(catMap, catStr)
	}

	var newCats []string
	for c := range catMap {
		newCats = append(newCats, c)
	}
	sort.Strings(newCats)
	newStr := strings.Join(newCats, ",")
	nowStr := time.Now().UTC().Format(time.RFC3339)

	res, err := tx.ExecContext(ctx, "UPDATE accounts SET disabled_categories = ?, updated_at = ? WHERE id = ?", newStr, nowStr, accountID)
	if err != nil {
		return fmt.Errorf("failed to update disabled_categories for account %s: %w", accountID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrAccountNotFound
	}

	return tx.Commit()
}
