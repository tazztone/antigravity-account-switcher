package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// Migration represents a versioned DDL change.
type Migration struct {
	Version     int
	Description string
	SQL         string
}

// Migrations contains all migration steps in sequential order.
var Migrations = []Migration{
	{
		Version:     1,
		Description: "initial_schema",
		SQL: `
-- 1. Migration tracking
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    description TEXT NOT NULL,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 2. Accounts table
CREATE TABLE IF NOT EXISTS accounts (
    id TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    refresh_token TEXT NOT NULL,
    access_token TEXT NOT NULL DEFAULT '',
    token_expiry DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00',
    is_active INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Partial unique index: enforces at most ONE account with is_active = 1
CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_single_active 
ON accounts(is_active) WHERE is_active = 1;

CREATE INDEX IF NOT EXISTS idx_accounts_status ON accounts(status);
CREATE INDEX IF NOT EXISTS idx_accounts_status_updated ON accounts(status, updated_at);

-- 3. Quota buckets table
CREATE TABLE IF NOT EXISTS quota_buckets (
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    bucket_id TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    window TEXT NOT NULL,
    remaining_fraction REAL NOT NULL DEFAULT 1.0,
    remaining_amount INTEGER NOT NULL DEFAULT 0,
    reset_time DATETIME NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (account_id, bucket_id)
);

CREATE INDEX IF NOT EXISTS idx_quota_buckets_account ON quota_buckets(account_id);
CREATE INDEX IF NOT EXISTS idx_quota_buckets_reset_time ON quota_buckets(reset_time);

-- 4. Token metrics table
CREATE TABLE IF NOT EXISTS token_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    request_path TEXT NOT NULL DEFAULT '',
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    candidates_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    cached_content_tokens INTEGER NOT NULL DEFAULT 0,
    thoughts_tokens INTEGER NOT NULL DEFAULT 0,
    timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_token_metrics_account_time ON token_metrics(account_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_token_metrics_timestamp ON token_metrics(timestamp);

-- 5. Proxy events table
CREATE TABLE IF NOT EXISTS proxy_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL,
    account_id TEXT REFERENCES accounts(id) ON DELETE SET NULL,
    message TEXT NOT NULL,
    details TEXT DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_proxy_events_created_at ON proxy_events(created_at);
`,
	},
	{
		Version:     2,
		Description: "active_category_accounts",
		SQL: `
-- 6. Model category active accounts table
CREATE TABLE IF NOT EXISTS active_category_accounts (
    category TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE
);
`,
	},
	{
		Version:     3,
		Description: "account_model_rules",
		SQL: `
-- 7. Per-account model eligibility and fallback routing rules
CREATE TABLE IF NOT EXISTS account_model_rules (
    account_id          TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    model_id            TEXT NOT NULL,
    display_name        TEXT NOT NULL DEFAULT '',
    category            TEXT NOT NULL DEFAULT 'unknown',
    enabled             INTEGER NOT NULL DEFAULT 1,
    fallback_account_id TEXT NOT NULL DEFAULT '',
    updated_at          TEXT NOT NULL,
    PRIMARY KEY (account_id, model_id)
);

CREATE INDEX IF NOT EXISTS idx_account_model_rules_model ON account_model_rules(model_id, enabled);
`,
	},
	{
		Version:     4,
		Description: "category_routing_mode_and_disabled_categories",
		SQL: `
-- 8. Add routing_mode to active_category_accounts and disabled_categories to accounts
ALTER TABLE active_category_accounts ADD COLUMN routing_mode TEXT NOT NULL DEFAULT 'pool';
ALTER TABLE accounts ADD COLUMN disabled_categories TEXT NOT NULL DEFAULT '';
`,
	},
	{
		Version:     5,
		Description: "account_model_rules_discovered",
		SQL: `
-- 9. Add discovered flag to distinguish discovered model catalog from explicit manual overrides
ALTER TABLE account_model_rules ADD COLUMN discovered INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_account_model_rules_discovered ON account_model_rules(account_id, discovered);
`,
	},
	{
		Version:     6,
		Description: "token_metrics_model_reasoning_conversation",
		SQL: `
-- 10. Add model, reasoning_mode, and conversation_id to token_metrics
ALTER TABLE token_metrics ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE token_metrics ADD COLUMN reasoning_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE token_metrics ADD COLUMN conversation_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_token_metrics_conversation ON token_metrics(conversation_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_token_metrics_model ON token_metrics(model, timestamp);
`,
	},
	{
		Version:     7,
		Description: "active_model_accounts",
		SQL: `
-- 11. Per-model priority account routing pins
CREATE TABLE IF NOT EXISTS active_model_accounts (
    model_id   TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_active_model_accounts_account ON active_model_accounts(account_id);
`,
	},
	{
		Version:     8,
		Description: "unify_gemini_argon_into_gemini",
		SQL: `
-- 12. Unify Gemini 4 Argon into shared Gemini category
UPDATE account_model_rules SET category = 'gemini' WHERE category = 'gemini_argon';
DELETE FROM active_category_accounts WHERE category = 'gemini_argon';
DELETE FROM quota_buckets WHERE LOWER(bucket_id) LIKE '%argon%' OR LOWER(display_name) LIKE '%argon%';
		UPDATE accounts
		SET disabled_categories = TRIM(REPLACE(',' || disabled_categories || ',', ',gemini_argon,', ','), ','),
		    updated_at = COALESCE(updated_at, CURRENT_TIMESTAMP)
		WHERE ',' || disabled_categories || ',' LIKE '%,gemini_argon,%';
`,
	},
	{
		Version:     9,
		Description: "plan_value_and_quota_snapshots",
		SQL: `
-- 13. Account plan metadata, extended token metrics, quota history snapshots, pricing overrides & competitor benchmarks
ALTER TABLE accounts ADD COLUMN plan_tier TEXT NOT NULL DEFAULT 'ai_pro';
ALTER TABLE accounts ADD COLUMN monthly_cost_usd REAL NOT NULL DEFAULT 20.0;

ALTER TABLE token_metrics ADD COLUMN tool_use_prompt_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE token_metrics ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS quota_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    bucket_id TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT 'unknown',
    window TEXT NOT NULL,
    remaining_fraction REAL NOT NULL,
    remaining_amount INTEGER NOT NULL DEFAULT 0,
    reset_time DATETIME NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'cloud_code',
    captured_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_quota_snapshots_acc_bucket_time
    ON quota_snapshots(account_id, bucket_id, captured_at);
CREATE INDEX IF NOT EXISTS idx_quota_snapshots_captured_at
    ON quota_snapshots(captured_at);

CREATE TABLE IF NOT EXISTS model_pricing_overrides (
    model_pattern TEXT PRIMARY KEY,
    display_name TEXT NOT NULL DEFAULT '',
    input_per_million REAL NOT NULL,
    cached_input_per_million REAL NOT NULL,
    cache_write_per_million REAL NOT NULL DEFAULT 0,
    output_per_million REAL NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS plan_benchmarks (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    plan_name TEXT NOT NULL,
    monthly_cost_usd REAL NOT NULL,
    window_5h_api_usd REAL NOT NULL DEFAULT 0,
    weekly_api_usd REAL NOT NULL DEFAULT 0,
    notes TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`,
	},
	{
		Version:     10,
		Description: "normalize_quota_snapshots_and_long_context_pricing",
		SQL: `
-- 14. Normalize snapshot window/category labels and add long-context multiplier columns to pricing overrides
UPDATE quota_snapshots
SET window = CASE
    WHEN LOWER(window) LIKE '%weekly%' OR LOWER(bucket_id) LIKE '%weekly%' THEN 'weekly'
    ELSE '5h'
END;

UPDATE quota_snapshots
SET category = CASE
    WHEN LOWER(bucket_id) LIKE '%claude%' OR LOWER(bucket_id) LIKE '%gpt%' OR LOWER(bucket_id) LIKE '%3p%'
      OR LOWER(display_name) LIKE '%claude%' OR LOWER(display_name) LIKE '%gpt%' OR LOWER(display_name) LIKE '%3p%'
      THEN 'claude_gpt'
    WHEN LOWER(bucket_id) LIKE '%gemini%' OR LOWER(display_name) LIKE '%gemini%'
      THEN 'gemini'
    ELSE COALESCE(NULLIF(category, ''), 'unknown')
END
WHERE category = '' OR category = 'unknown';

CREATE INDEX IF NOT EXISTS idx_quota_snapshots_acc_cat_win_time
    ON quota_snapshots(account_id, category, window, captured_at);

ALTER TABLE model_pricing_overrides ADD COLUMN long_context_threshold INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_pricing_overrides ADD COLUMN long_context_input_multiplier REAL NOT NULL DEFAULT 1.0;
ALTER TABLE model_pricing_overrides ADD COLUMN long_context_output_multiplier REAL NOT NULL DEFAULT 1.0;
`,
	},
}

// Migrate applies all pending schema migrations to the database.
func Migrate(db *DB) error {
	ctx := context.Background()

	// Ensure schema_migrations exists first
	initSQL := `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		description TEXT NOT NULL,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := db.ExecContext(ctx, initSQL); err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	for _, m := range Migrations {
		var count int
		err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.Version).Scan(&count)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("failed to query migration version %d: %w", m.Version, err)
		}
		if count > 0 {
			continue // Already applied
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin tx for migration %d: %w", m.Version, err)
		}

		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to execute migration %d (%s): %w", m.Version, m.Description, err)
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, description) VALUES (?, ?)", m.Version, m.Description); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record migration %d: %w", m.Version, err)
		}

		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to commit migration %d: %w", m.Version, err)
		}
	}

	// Seed active_category_accounts if empty and an active account exists
	seedTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx for category seeding: %w", err)
	}
	seedSQL := `
	INSERT OR IGNORE INTO active_category_accounts (category, account_id)
	SELECT 'claude_gpt', id FROM accounts WHERE is_active = 1
	UNION ALL
	SELECT 'gemini', id FROM accounts WHERE is_active = 1;`
	if _, err := seedTx.ExecContext(ctx, seedSQL); err != nil {
		_ = seedTx.Rollback()
		return fmt.Errorf("failed to seed active_category_accounts: %w", err)
	}
	if err := seedTx.Commit(); err != nil {
		_ = seedTx.Rollback()
		return fmt.Errorf("failed to commit active_category_accounts seeding: %w", err)
	}

	return nil
}
