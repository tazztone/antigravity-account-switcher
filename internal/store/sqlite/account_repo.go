package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
			is_active, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
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
	)
	if err != nil {
		if strings.Contains(err.Error(), "accounts.email") && strings.Contains(err.Error(), "UNIQUE") {
			return domain.ErrAccountEmailExists
		}
		return fmt.Errorf("failed to create account: %w", err)
	}

	if acc.IsActive {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO active_category_accounts (category, account_id) VALUES ('claude_gpt', ?), ('gemini', ?)
			ON CONFLICT(category) DO UPDATE SET account_id = excluded.account_id
		`, acc.ID, acc.ID); err != nil {
			return fmt.Errorf("failed to seed active_category_accounts on create: %w", err)
		}
	}

	return tx.Commit()
}

// GetByID retrieves an account by ID.
func (r *AccountRepository) GetByID(ctx context.Context, id string) (*domain.Account, error) {
	query := `
		SELECT id, email, refresh_token, access_token, token_expiry, is_active, status, created_at, updated_at
		FROM accounts
		WHERE id = ?
	`
	row := r.db.QueryRowContext(ctx, query, id)
	return r.scanAccount(row)
}

// GetByEmail retrieves an account by email.
func (r *AccountRepository) GetByEmail(ctx context.Context, email string) (*domain.Account, error) {
	query := `
		SELECT id, email, refresh_token, access_token, token_expiry, is_active, status, created_at, updated_at
		FROM accounts
		WHERE email = ?
	`
	row := r.db.QueryRowContext(ctx, query, email)
	return r.scanAccount(row)
}

// GetActive retrieves the single currently active account.
func (r *AccountRepository) GetActive(ctx context.Context) (*domain.Account, error) {
	query := `
		SELECT id, email, refresh_token, access_token, token_expiry, is_active, status, created_at, updated_at
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
		SELECT id, email, refresh_token, access_token, token_expiry, is_active, status, created_at, updated_at
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

	// Step 3: Populate active_category_accounts for both categories
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO active_category_accounts (category, account_id) VALUES ('claude_gpt', ?), ('gemini', ?)
		ON CONFLICT(category) DO UPDATE SET account_id = excluded.account_id
	`, id, id); err != nil {
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
		SELECT id, email, refresh_token, access_token, token_expiry, is_active, status, created_at, updated_at
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
// its reset time), the pin is advanced to the next eligible account so traffic
// does not keep hitting a known-dead account; when no candidate exists the dead
// pin is returned unchanged, preserving pre-fix behavior.
func (r *AccountRepository) GetActiveForCategory(ctx context.Context, category domain.ModelCategory) (*domain.Account, error) {
	catStr := category.String()
	if catStr == "unknown" || catStr == "" {
		return r.GetActive(ctx)
	}

	query := `
		SELECT a.id, a.email, a.refresh_token, a.access_token, a.token_expiry, a.is_active, a.status, a.created_at, a.updated_at
		FROM accounts a
		JOIN active_category_accounts aca ON a.id = aca.account_id
		WHERE aca.category = ?
		LIMIT 1
	`
	row := r.db.QueryRowContext(ctx, query, catStr)
	acc, err := r.scanAccount(row)
	if err == nil {
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
		globalActive, gErr := r.GetActive(ctx)
		if gErr == nil && globalActive != nil {
			_, _ = r.db.ExecContext(ctx, `
				INSERT OR IGNORE INTO active_category_accounts (category, account_id)
				VALUES (?, ?)
			`, catStr, globalActive.ID)
			return globalActive, nil
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
//   - 'active' accounts are eligible unless a recorded bucket for the category
//     is exhausted and not yet past reset.
//   - 'exhausted' accounts are eligible only when quota rows positively show a
//     healthy bucket for this category: a 429 in one category must not lock the
//     account out of the other, but a bare 'exhausted' status without fresh
//     quota evidence keeps the account excluded.
//   - 'error' and 'disabled' accounts are never eligible.
func (r *AccountRepository) GetNextAvailableForCategory(ctx context.Context, category domain.ModelCategory, excludeID string) (*domain.Account, error) {
	catStr := category.String()
	if catStr == "unknown" || catStr == "" {
		return r.GetNextAvailable(ctx, excludeID)
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	query := `
		SELECT a.id, a.email, a.refresh_token, a.access_token, a.token_expiry, a.is_active, a.status, a.created_at, a.updated_at
		FROM accounts a
		WHERE (? = '' OR a.id != ?)
		  AND (
			a.status = 'active'
			OR (
				a.status = 'exhausted'
				AND EXISTS (
					SELECT 1 FROM quota_buckets qb
					WHERE qb.account_id = a.id
					  AND qb.remaining_fraction > 0.0
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
		ORDER BY a.updated_at ASC
		LIMIT 1
	`
	row := r.db.QueryRowContext(ctx, query, excludeID, excludeID, catStr, catStr, catStr, nowStr, catStr, catStr, catStr)
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
	var statusStr, expiryStr, createdStr, updatedStr string
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
		INSERT INTO account_model_rules (account_id, model_id, display_name, category, enabled, fallback_account_id, updated_at)
		VALUES (?, ?, ?, ?, 1, '', ?)
		ON CONFLICT(account_id, model_id) DO UPDATE SET
			display_name = excluded.display_name,
			category = excluded.category,
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare upsert: %w", err)
	}
	defer stmt.Close()

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
	}

	return tx.Commit()
}

// ListAccountModelRules returns per-model eligibility rules for an account.
func (r *AccountRepository) ListAccountModelRules(ctx context.Context, accountID string) ([]*domain.AccountModelRule, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT account_id, model_id, display_name, category, enabled, fallback_account_id, updated_at
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
		if err := rows.Scan(&rule.AccountID, &rule.ModelID, &rule.DisplayName, &rule.Category, &enabledInt, &rule.FallbackAccountID, &updatedStr); err != nil {
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

// UpdateAccountModelRule upserts the enabled flag and fallback target for one
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
		INSERT INTO account_model_rules (account_id, model_id, display_name, category, enabled, fallback_account_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, model_id) DO UPDATE SET
			enabled = excluded.enabled,
			fallback_account_id = excluded.fallback_account_id,
			updated_at = excluded.updated_at
	`, rule.AccountID, rule.ModelID, rule.DisplayName, rule.Category, enabledInt, rule.FallbackAccountID, nowStr)
	if err != nil {
		return fmt.Errorf("failed to update model rule %s: %w", rule.ModelID, err)
	}

	return tx.Commit()
}

// IsAccountEligibleForModel reports whether an account may serve a model, plus
// its configured fallback target. Fail-open: an account with no model rules at
// all is eligible for everything; once rules exist, the model must be present
// with enabled = 1.
func (r *AccountRepository) IsAccountEligibleForModel(ctx context.Context, accountID, modelID string) (bool, string, error) {
	if accountID == "" {
		return false, domain.FallbackAccountAuto, domain.ErrAccountNotFound
	}
	if modelID == "" {
		return true, domain.FallbackAccountAuto, nil
	}

	var ruleCount int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_model_rules WHERE account_id = ?", accountID).Scan(&ruleCount); err != nil {
		return false, domain.FallbackAccountAuto, fmt.Errorf("failed to count model rules: %w", err)
	}
	if ruleCount == 0 {
		return true, domain.FallbackAccountAuto, nil
	}

	var enabledInt int
	var fallbackID string
	err := r.db.QueryRowContext(ctx, `
		SELECT enabled, fallback_account_id FROM account_model_rules
		WHERE account_id = ? AND model_id = ?
	`, accountID, modelID).Scan(&enabledInt, &fallbackID)
	if errors.Is(err, sql.ErrNoRows) {
		// Catalog populated but this model never discovered on this account.
		return false, domain.FallbackAccountAuto, nil
	}
	if err != nil {
		return false, domain.FallbackAccountAuto, fmt.Errorf("failed to query model rule: %w", err)
	}
	return enabledInt == 1, fallbackID, nil
}

// GetNextAvailableForModel selects the next account eligible for a specific model:
// active status, not exhausted for the category, not excluded, and either without
// model rules or with the model present and enabled.
func (r *AccountRepository) GetNextAvailableForModel(ctx context.Context, category domain.ModelCategory, modelID, excludeID string) (*domain.Account, error) {
	// Base candidate query mirrors GetNextAvailableForCategory (category quota +
	// status), then adds the per-account model eligibility constraint.
	next, err := r.GetNextAvailableForCategory(ctx, category, excludeID)
	if err != nil || next == nil {
		return next, err
	}

	eligible, _, err := r.IsAccountEligibleForModel(ctx, next.ID, modelID)
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
		SELECT a.id, a.email, a.refresh_token, a.access_token, a.token_expiry, a.is_active, a.status, a.created_at, a.updated_at
		FROM accounts a
		WHERE (? = '' OR a.id != ?)
		  AND (
			a.status = 'active'
			OR (
				a.status = 'exhausted'
				AND EXISTS (
					SELECT 1 FROM quota_buckets qb
					WHERE qb.account_id = a.id
					  AND qb.remaining_fraction > 0.0
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
		  AND (
			NOT EXISTS (SELECT 1 FROM account_model_rules mr WHERE mr.account_id = a.id)
			OR EXISTS (
				SELECT 1 FROM account_model_rules mr
				WHERE mr.account_id = a.id AND mr.model_id = ? AND mr.enabled = 1
			)
		  )
		ORDER BY a.updated_at ASC
		LIMIT 1
	`
	args := []any{excludeID, excludeID, catStr, catStr, catStr, nowStr, catStr, catStr, catStr, modelID}
	row := r.db.QueryRowContext(ctx, query, args...)
	acc, err := r.scanAccount(row)
	if errors.Is(err, domain.ErrAccountNotFound) {
		return nil, domain.ErrNoAvailableAccount
	}
	return acc, err
}
