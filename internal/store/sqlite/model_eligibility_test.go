package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/store/sqlite"
)

func newEligibilityTestRepo(t *testing.T) (context.Context, *sqlite.DB, *sqlite.AccountRepository) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "elig_test.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return context.Background(), db, sqlite.NewAccountRepository(db)
}

func createEligibilityAccount(t *testing.T, repo *sqlite.AccountRepository, ctx context.Context, id, email string, active bool) {
	t.Helper()
	acc := &domain.Account{
		ID:       id,
		Email:    email,
		IsActive: active,
		Status:   domain.AccountStatusActive,
	}
	if err := repo.Create(ctx, acc); err != nil {
		t.Fatalf("Create %s: %v", id, err)
	}
}

func TestSchemaV3_AccountModelRulesTable(t *testing.T) {
	ctx, db, _ := newEligibilityTestRepo(t)

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = 3").Scan(&count); err != nil {
		t.Fatalf("query migration v3: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected migration 3 applied, got %d", count)
	}

	// Table exists with composite PK; FK rejects rows for unknown accounts
	if _, err := db.ExecContext(ctx, `
		INSERT INTO account_model_rules (account_id, model_id, updated_at) VALUES ('ghost', 'm', '2026-01-01')
	`); err == nil {
		t.Fatal("expected FK violation for unknown account_id")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO accounts (id, email, refresh_token) VALUES ('a', 'a@example.com', 'tok')
	`); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO account_model_rules (account_id, model_id, updated_at) VALUES ('a', 'm', '2026-01-01')
	`); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO account_model_rules (account_id, model_id, updated_at) VALUES ('a', 'm', '2026-01-02')
	`); err == nil {
		t.Fatal("expected duplicate (account_id, model_id) insert to violate PK")
	}
}

func TestUpsertAccountModels_PreservesUserOverrides(t *testing.T) {
	ctx, _, repo := newEligibilityTestRepo(t)
	createEligibilityAccount(t, repo, ctx, "acc-paid", "paid@example.com", true)

	first := []*domain.ModelInfo{
		{ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5", Category: "claude_gpt"},
		{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Category: "gemini"},
	}
	if err := repo.UpsertAccountModels(ctx, "acc-paid", first); err != nil {
		t.Fatalf("UpsertAccountModels: %v", err)
	}

	// User disables opus and pins a fallback target
	if err := repo.UpdateAccountModelRule(ctx, &domain.AccountModelRule{
		AccountID:   "acc-paid",
		ModelID:     "claude-opus-5-5",
		DisplayName: "Claude Opus 5.5",
		Category:    "claude_gpt",
		Enabled:     false,
	}); err != nil {
		t.Fatalf("UpdateAccountModelRule: %v", err)
	}

	// Re-discovery must not clobber the user's overrides
	second := []*domain.ModelInfo{
		{ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5 (renamed)", Category: "claude_gpt"},
		{ID: "gemini-4-argon-medium", DisplayName: "Gemini 4 Argon Medium", Category: "gemini_argon"},
	}
	if err := repo.UpsertAccountModels(ctx, "acc-paid", second); err != nil {
		t.Fatalf("UpsertAccountModels second pass: %v", err)
	}

	rules, err := repo.ListAccountModelRules(ctx, "acc-paid")
	if err != nil {
		t.Fatalf("ListAccountModelRules: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}

	byModel := map[string]*domain.AccountModelRule{}
	for _, r := range rules {
		byModel[r.ModelID] = r
	}
	opus := byModel["claude-opus-5-5"]
	if opus == nil || opus.Enabled {
		t.Errorf("opus override lost: %+v", opus)
	}
	if opus != nil && opus.DisplayName != "Claude Opus 5.5 (renamed)" {
		t.Errorf("expected display name refreshed on conflict, got %q", opus.DisplayName)
	}
	argon := byModel["gemini-4-argon-medium"]
	if argon == nil || !argon.Enabled || argon.Category != "gemini_argon" {
		t.Errorf("new model not inserted enabled: %+v", argon)
	}
}

func TestIsAccountEligibleForModel_FailOpenWithoutRules(t *testing.T) {
	ctx, _, repo := newEligibilityTestRepo(t)
	createEligibilityAccount(t, repo, ctx, "acc-1", "one@example.com", true)

	eligible, err := repo.IsAccountEligibleForModel(ctx, "acc-1", "claude-opus-5-5")
	if err != nil {
		t.Fatalf("IsAccountEligibleForModel: %v", err)
	}
	if !eligible {
		t.Errorf("expected fail-open eligibility, got eligible=%v", eligible)
	}
}

func TestIsAccountEligibleForModel_DiscoveredModelMissing(t *testing.T) {
	ctx, _, repo := newEligibilityTestRepo(t)
	createEligibilityAccount(t, repo, ctx, "acc-1", "one@example.com", true)

	// Catalog populated without the requested model -> model not eligible
	if err := repo.UpsertAccountModels(ctx, "acc-1", []*domain.ModelInfo{
		{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Category: "gemini"},
	}); err != nil {
		t.Fatalf("UpsertAccountModels: %v", err)
	}

	eligible, err := repo.IsAccountEligibleForModel(ctx, "acc-1", "claude-opus-5-5")
	if err != nil {
		t.Fatalf("IsAccountEligibleForModel: %v", err)
	}
	if eligible {
		t.Error("expected ineligible for model absent from populated catalog")
	}

	eligible, err = repo.IsAccountEligibleForModel(ctx, "acc-1", "gemini-2.5-pro")
	if err != nil {
		t.Fatalf("IsAccountEligibleForModel: %v", err)
	}
	if !eligible {
		t.Error("expected eligible for discovered model")
	}
}

func TestGetNextAvailableForModel_SkipsDisabledModel(t *testing.T) {
	ctx, _, repo := newEligibilityTestRepo(t)
	createEligibilityAccount(t, repo, ctx, "acc-paid", "paid@example.com", true)
	createEligibilityAccount(t, repo, ctx, "acc-trial", "trial@example.com", false)

	// Paid catalog: opus disabled; Trial catalog: opus enabled
	if err := repo.UpsertAccountModels(ctx, "acc-paid", []*domain.ModelInfo{
		{ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5", Category: "claude_gpt"},
		{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Category: "gemini"},
	}); err != nil {
		t.Fatalf("UpsertAccountModels paid: %v", err)
	}
	if err := repo.UpdateAccountModelRule(ctx, &domain.AccountModelRule{
		AccountID: "acc-paid",
		ModelID:   "claude-opus-5-5",
		Category:  "claude_gpt",
		Enabled:   false,
	}); err != nil {
		t.Fatalf("UpdateAccountModelRule: %v", err)
	}
	if err := repo.UpsertAccountModels(ctx, "acc-trial", []*domain.ModelInfo{
		{ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5", Category: "claude_gpt"},
	}); err != nil {
		t.Fatalf("UpsertAccountModels trial: %v", err)
	}

	next, err := repo.GetNextAvailableForModel(ctx, domain.CategoryClaudeGPT, "claude-opus-5-5", "")
	if err != nil {
		t.Fatalf("GetNextAvailableForModel: %v", err)
	}
	if next == nil || next.ID != "acc-trial" {
		t.Fatalf("expected rotation to acc-trial, got %+v (err=%v)", next, err)
	}

	// Gemini remains eligible on paid despite opus being disabled
	next, err = repo.GetNextAvailableForModel(ctx, domain.CategoryGemini, "gemini-2.5-pro", "acc-trial")
	if err != nil {
		t.Fatalf("GetNextAvailableForModel gemini: %v", err)
	}
	if next == nil || next.ID != "acc-paid" {
		t.Fatalf("expected acc-paid for gemini, got %+v", next)
	}
}

func TestGetNextAvailableForModel_NoEligibleAccount(t *testing.T) {
	ctx, _, repo := newEligibilityTestRepo(t)
	createEligibilityAccount(t, repo, ctx, "acc-1", "one@example.com", true)

	// Only account has the model disabled
	if err := repo.UpsertAccountModels(ctx, "acc-1", []*domain.ModelInfo{
		{ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5", Category: "claude_gpt"},
	}); err != nil {
		t.Fatalf("UpsertAccountModels: %v", err)
	}
	if err := repo.UpdateAccountModelRule(ctx, &domain.AccountModelRule{
		AccountID: "acc-1",
		ModelID:   "claude-opus-5-5",
		Category:  "claude_gpt",
		Enabled:   false,
	}); err != nil {
		t.Fatalf("UpdateAccountModelRule: %v", err)
	}

	_, err := repo.GetNextAvailableForModel(ctx, domain.CategoryClaudeGPT, "claude-opus-5-5", "")
	if !errors.Is(err, domain.ErrNoAvailableAccount) {
		t.Fatalf("expected ErrNoAvailableAccount, got %v", err)
	}
}

func TestSetActiveForCategory_DoesNotFlipGlobalActive(t *testing.T) {
	ctx, _, repo := newEligibilityTestRepo(t)
	createEligibilityAccount(t, repo, ctx, "acc-a", "a@example.com", true)
	createEligibilityAccount(t, repo, ctx, "acc-b", "b@example.com", false)

	if err := repo.SetActiveForCategory(ctx, domain.CategoryClaudeGPT, "acc-b"); err != nil {
		t.Fatalf("SetActiveForCategory: %v", err)
	}

	// acc-b holds the claude_gpt pin...
	pinned, err := repo.GetActiveForCategory(ctx, domain.CategoryClaudeGPT)
	if err != nil {
		t.Fatalf("GetActiveForCategory: %v", err)
	}
	if pinned.ID != "acc-b" {
		t.Errorf("expected claude_gpt pin acc-b, got %s", pinned.ID)
	}

	// ...but the global primary IDE account stays acc-a
	active, err := repo.GetActive(ctx)
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if active.ID != "acc-a" {
		t.Errorf("expected global active acc-a after category pin switch, got %s", active.ID)
	}
	a, err := repo.GetByID(ctx, "acc-a")
	if err != nil {
		t.Fatalf("GetByID acc-a: %v", err)
	}
	if !a.IsActive {
		t.Error("expected acc-a to keep is_active=1 after SetActiveForCategory")
	}
	b, err := repo.GetByID(ctx, "acc-b")
	if err != nil {
		t.Fatalf("GetByID acc-b: %v", err)
	}
	if b.IsActive {
		t.Error("expected acc-b is_active=0 after SetActiveForCategory (global untouched)")
	}
}

func TestSetActiveForCategory_SetsGlobalActiveWhenExplicit(t *testing.T) {
	ctx, _, repo := newEligibilityTestRepo(t)
	createEligibilityAccount(t, repo, ctx, "acc-a", "a@example.com", true)
	createEligibilityAccount(t, repo, ctx, "acc-b", "b@example.com", false)

	// unknown category delegates to SetActive (global)
	if err := repo.SetActiveForCategory(ctx, domain.CategoryUnknown, "acc-b"); err != nil {
		t.Fatalf("SetActiveForCategory(unknown): %v", err)
	}
	active, err := repo.GetActive(ctx)
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if active.ID != "acc-b" {
		t.Errorf("expected global active acc-b, got %s", active.ID)
	}
}

func TestGetActiveCategoriesByAccount(t *testing.T) {
	ctx, _, repo := newEligibilityTestRepo(t)
	createEligibilityAccount(t, repo, ctx, "acc-a", "a@example.com", true)
	createEligibilityAccount(t, repo, ctx, "acc-b", "b@example.com", false)

	if err := repo.SetActiveForCategory(ctx, domain.CategoryGeminiArgon, "acc-b"); err != nil {
		t.Fatalf("SetActiveForCategory: %v", err)
	}

	cats, err := repo.GetActiveCategoriesByAccount(ctx)
	if err != nil {
		t.Fatalf("GetActiveCategoriesByAccount: %v", err)
	}

	bCats := cats["acc-b"]
	if len(bCats) != 1 || bCats[0] != domain.CategoryGeminiArgon {
		t.Errorf("expected [gemini_argon] for acc-b, got %v", bCats)
	}
	// acc-a was created active, then lost the argon pin to acc-b: claude_gpt + gemini remain
	aCats := cats["acc-a"]
	if len(aCats) != 2 {
		t.Errorf("expected [claude_gpt gemini] for acc-a after argon pin moved, got %v", aCats)
	}
	for _, c := range aCats {
		if c == domain.CategoryGeminiArgon {
			t.Errorf("argon pin should only be on acc-b, got %v", aCats)
		}
	}
}

func TestGeminiArgonBucketIsolation(t *testing.T) {
	ctx, db, repo := newEligibilityTestRepo(t)
	createEligibilityAccount(t, repo, ctx, "acc-1", "one@example.com", true)

	reset := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	// Regular gemini 5h bucket exhausted; argon weekly bucket healthy
	upsert := func(bucketID, displayName, window string, fraction float64) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO quota_buckets (account_id, bucket_id, display_name, window, remaining_fraction, reset_time, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, "acc-1", bucketID, displayName, window, fraction, reset, reset); err != nil {
			t.Fatalf("seed bucket %s: %v", bucketID, err)
		}
	}
	upsert("gemini-5h", "Gemini (5h)", "5h", 0.0)
	upsert("argon-weekly", "Gemini Argon (weekly)", "weekly", 1.0)

	// Regular gemini exhausted: acc-1 excluded for gemini...
	next, err := repo.GetNextAvailableForCategory(ctx, domain.CategoryGemini, "")
	if !errors.Is(err, domain.ErrNoAvailableAccount) {
		t.Fatalf("expected no available gemini account, got %+v err=%v", next, err)
	}

	// ...but still eligible for gemini_argon (its own healthy weekly bucket).
	// Before the predicate fix, '%gemini%' on display_name "Gemini Argon (weekly)"
	// masked the exhausted regular bucket and the account stayed eligible for gemini;
	// the isolated argon check now governs.
	next, err = repo.GetNextAvailableForCategory(ctx, domain.CategoryGeminiArgon, "")
	if err != nil {
		t.Fatalf("GetNextAvailableForCategory(argon): %v", err)
	}
	if next == nil || next.ID != "acc-1" {
		t.Fatalf("expected acc-1 eligible for argon, got %+v", next)
	}

	// Reverse: exhausted argon weekly bucket must not lock out regular gemini.
	if _, err := db.ExecContext(ctx, `UPDATE quota_buckets SET remaining_fraction = 0.0 WHERE bucket_id = 'argon-weekly'`); err != nil {
		t.Fatalf("exhaust argon: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE quota_buckets SET remaining_fraction = 1.0 WHERE bucket_id = 'gemini-5h'`); err != nil {
		t.Fatalf("restore gemini: %v", err)
	}
	next, err = repo.GetNextAvailableForCategory(ctx, domain.CategoryGemini, "")
	if err != nil {
		t.Fatalf("GetNextAvailableForCategory(gemini): %v", err)
	}
	if next == nil || next.ID != "acc-1" {
		t.Fatalf("expected acc-1 eligible for gemini after argon exhaustion, got %+v", next)
	}
	next, err = repo.GetNextAvailableForCategory(ctx, domain.CategoryGeminiArgon, "")
	if !errors.Is(err, domain.ErrNoAvailableAccount) {
		t.Fatalf("expected no available argon account, got %+v err=%v", next, err)
	}
}

func TestIsAccountAvailableForCategory(t *testing.T) {
	ctx, db, repo := newEligibilityTestRepo(t)
	createEligibilityAccount(t, repo, ctx, "acc-1", "one@example.com", true)

	reset := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	seedBucket := func(accountID, bucketID, displayName, window string, fraction float64) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO quota_buckets (account_id, bucket_id, display_name, window, remaining_fraction, reset_time, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, accountID, bucketID, displayName, window, fraction, reset, reset); err != nil {
			t.Fatalf("seed bucket %s: %v", bucketID, err)
		}
	}
	assertAvailable := func(category domain.ModelCategory, want bool) {
		t.Helper()
		got, err := repo.IsAccountAvailableForCategory(ctx, "acc-1", category)
		if err != nil {
			t.Fatalf("IsAccountAvailableForCategory(%s): %v", category, err)
		}
		if got != want {
			t.Errorf("category %s: expected available=%v, got %v", category, want, got)
		}
	}

	// Active account without quota rows is available everywhere.
	assertAvailable(domain.CategoryClaudeGPT, true)
	assertAvailable(domain.CategoryUnknown, true)

	// Exhausted claude bucket only blocks the claude category.
	seedBucket("acc-1", "claude-5h", "Claude (5h)", "5h", 0.0)
	seedBucket("acc-1", "gemini-5h", "Gemini (5h)", "5h", 1.0)
	assertAvailable(domain.CategoryClaudeGPT, false)
	assertAvailable(domain.CategoryGemini, true)

	// An 'exhausted' global status stays usable while a healthy category bucket exists.
	if err := repo.UpdateStatus(ctx, "acc-1", domain.AccountStatusExhausted); err != nil {
		t.Fatalf("UpdateStatus exhausted: %v", err)
	}
	assertAvailable(domain.CategoryGemini, true)
	assertAvailable(domain.CategoryClaudeGPT, false)
	// Unknown category has no quota evidence, so it follows the raw status.
	assertAvailable(domain.CategoryUnknown, false)

	// Disabled accounts are never available.
	if err := repo.UpdateStatus(ctx, "acc-1", domain.AccountStatusDisabled); err != nil {
		t.Fatalf("UpdateStatus disabled: %v", err)
	}
	assertAvailable(domain.CategoryGemini, false)
	assertAvailable(domain.CategoryUnknown, false)
}

func TestArgonEntitlement_EligibilityAndRouting(t *testing.T) {
	ctx, db, repo := newEligibilityTestRepo(t)

	// acc-unentitled has active status but no quota buckets and no model rules.
	createEligibilityAccount(t, repo, ctx, "acc-unentitled", "free@example.com", true)

	// Available for standard categories, but NOT for gemini_argon.
	available, err := repo.IsAccountAvailableForCategory(ctx, "acc-unentitled", domain.CategoryGemini)
	if err != nil || !available {
		t.Fatalf("expected available for regular gemini, got %v err=%v", available, err)
	}
	available, err = repo.IsAccountAvailableForCategory(ctx, "acc-unentitled", domain.CategoryClaudeGPT)
	if err != nil || !available {
		t.Fatalf("expected available for claude_gpt, got %v err=%v", available, err)
	}
	available, err = repo.IsAccountAvailableForCategory(ctx, "acc-unentitled", domain.CategoryGeminiArgon)
	if err != nil || available {
		t.Fatalf("expected unavailable for gemini_argon without entitlement, got %v err=%v", available, err)
	}

	// GetNextAvailableForCategory / Model must exclude acc-unentitled.
	next, err := repo.GetNextAvailableForCategory(ctx, domain.CategoryGeminiArgon, "")
	if !errors.Is(err, domain.ErrNoAvailableAccount) {
		t.Fatalf("expected ErrNoAvailableAccount for unentitled pool, got %+v err=%v", next, err)
	}
	next, err = repo.GetNextAvailableForModel(ctx, domain.CategoryGeminiArgon, "gemini-4-argon-high", "")
	if !errors.Is(err, domain.ErrNoAvailableAccount) {
		t.Fatalf("expected ErrNoAvailableAccount for model on unentitled pool, got %+v err=%v", next, err)
	}

	// Add acc-entitled with an Argon quota bucket.
	createEligibilityAccount(t, repo, ctx, "acc-entitled", "pro@example.com", false)
	reset := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quota_buckets (account_id, bucket_id, display_name, window, remaining_fraction, reset_time, updated_at)
		VALUES ('acc-entitled', 'argon-weekly', 'Gemini 4 Argon Weekly Limit', 'weekly', 0.8, ?, ?)
	`, reset, reset); err != nil {
		t.Fatalf("seed argon bucket: %v", err)
	}

	available, err = repo.IsAccountAvailableForCategory(ctx, "acc-entitled", domain.CategoryGeminiArgon)
	if err != nil || !available {
		t.Fatalf("expected acc-entitled available for gemini_argon, got %v err=%v", available, err)
	}

	next, err = repo.GetNextAvailableForCategory(ctx, domain.CategoryGeminiArgon, "")
	if err != nil || next == nil || next.ID != "acc-entitled" {
		t.Fatalf("expected acc-entitled selected for gemini_argon, got %+v err=%v", next, err)
	}
	next, err = repo.GetNextAvailableForModel(ctx, domain.CategoryGeminiArgon, "gemini-4-argon-high", "")
	if err != nil || next == nil || next.ID != "acc-entitled" {
		t.Fatalf("expected acc-entitled selected for argon model, got %+v err=%v", next, err)
	}

	// Add acc-rules with an enabled Argon model rule (no bucket yet).
	createEligibilityAccount(t, repo, ctx, "acc-rules", "rules@example.com", false)
	if err := repo.UpsertAccountModels(ctx, "acc-rules", []*domain.ModelInfo{
		{ID: "gemini-4-argon-high", DisplayName: "Gemini 4 Argon (High)", Category: "gemini_argon"},
	}); err != nil {
		t.Fatalf("UpsertAccountModels: %v", err)
	}

	available, err = repo.IsAccountAvailableForCategory(ctx, "acc-rules", domain.CategoryGeminiArgon)
	if err != nil || !available {
		t.Fatalf("expected acc-rules available for gemini_argon via model rule, got %v err=%v", available, err)
	}

	// Add acc-pro with a Pro model rule (claude-opus-5-5-high)
	createEligibilityAccount(t, repo, ctx, "acc-pro", "pro-user@example.com", false)
	if err := repo.UpsertAccountModels(ctx, "acc-pro", []*domain.ModelInfo{
		{ID: "claude-opus-5-5-high", DisplayName: "Claude Opus 5.5 (High)", Category: "claude_gpt"},
	}); err != nil {
		t.Fatalf("UpsertAccountModels: %v", err)
	}

	available, err = repo.IsAccountAvailableForCategory(ctx, "acc-pro", domain.CategoryGeminiArgon)
	if err != nil || !available {
		t.Fatalf("expected acc-pro available for gemini_argon via 5.5 model rule, got %v err=%v", available, err)
	}

	// Internal/tab/preview models are always eligible, even when disabled in DB rules
	if err := repo.UpdateAccountModelRule(ctx, &domain.AccountModelRule{
		AccountID: "acc-pro",
		ModelID:   "tab_flash_lite_preview",
		Enabled:   false,
	}); err != nil {
		t.Fatalf("UpdateAccountModelRule: %v", err)
	}
	isEligible, err := repo.IsAccountEligibleForModel(ctx, "acc-pro", "tab_flash_lite_preview")
	if err != nil || !isEligible {
		t.Fatalf("expected internal model tab_flash_lite_preview to always be eligible, got eligible=%v, err=%v", isEligible, err)
	}
}

func TestModelEligibility_ManualOverrideWithoutDiscoveredCatalog(t *testing.T) {
	ctx, _, repo := newEligibilityTestRepo(t)

	createEligibilityAccount(t, repo, ctx, "acc-manual", "manual@example.com", true)

	// User manually disables one model without running full catalog discovery
	if err := repo.UpdateAccountModelRule(ctx, &domain.AccountModelRule{
		AccountID: "acc-manual",
		ModelID:   "claude-3-5-haiku",
		Enabled:   false,
	}); err != nil {
		t.Fatalf("UpdateAccountModelRule: %v", err)
	}

	// The explicitly disabled model must be ineligible
	eligible, err := repo.IsAccountEligibleForModel(ctx, "acc-manual", "claude-3-5-haiku")
	if err != nil {
		t.Fatalf("IsAccountEligibleForModel: %v", err)
	}
	if eligible {
		t.Errorf("expected claude-3-5-haiku to be ineligible when disabled")
	}

	// Other models must remain fail-open / eligible
	eligibleOther, err := repo.IsAccountEligibleForModel(ctx, "acc-manual", "claude-3-7-sonnet")
	if err != nil {
		t.Fatalf("IsAccountEligibleForModel: %v", err)
	}
	if !eligibleOther {
		t.Errorf("expected claude-3-7-sonnet to remain eligible when no discovered catalog exists")
	}

	// Explicit disable on a preview model (not tab/chat) must be respected
	if err := repo.UpdateAccountModelRule(ctx, &domain.AccountModelRule{
		AccountID: "acc-manual",
		ModelID:   "o1-preview",
		Enabled:   false,
	}); err != nil {
		t.Fatalf("UpdateAccountModelRule: %v", err)
	}
	eligiblePreview, err := repo.IsAccountEligibleForModel(ctx, "acc-manual", "o1-preview")
	if err != nil {
		t.Fatalf("IsAccountEligibleForModel: %v", err)
	}
	if eligiblePreview {
		t.Errorf("expected o1-preview to be ineligible when explicitly disabled")
	}
}

func TestCategoryRouting_OptOutInSoloMode(t *testing.T) {
	ctx, _, repo := newEligibilityTestRepo(t)

	createEligibilityAccount(t, repo, ctx, "acc-solo-optout", "solo-optout@example.com", true)

	// Pin account to claude_gpt in solo mode
	if err := repo.SetActiveForCategory(ctx, domain.CategoryClaudeGPT, "acc-solo-optout"); err != nil {
		t.Fatalf("SetActiveForCategory: %v", err)
	}
	if err := repo.SetCategoryRoutingMode(ctx, domain.CategoryClaudeGPT, domain.RoutingModeSolo); err != nil {
		t.Fatalf("SetCategoryRoutingMode: %v", err)
	}

	// Disable claude_gpt on this account
	if err := repo.SetAccountCategoryDisabled(ctx, "acc-solo-optout", domain.CategoryClaudeGPT, true); err != nil {
		t.Fatalf("SetAccountCategoryDisabled: %v", err)
	}

	// GetActiveForCategory must return ErrNoActiveAccount, not the opted-out account
	active, err := repo.GetActiveForCategory(ctx, domain.CategoryClaudeGPT)
	if err == nil || active != nil {
		t.Errorf("expected ErrNoActiveAccount for disabled category in solo mode, got active=%v, err=%v", active, err)
	}
}
