package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

func TestCategoryRoutingAndExclusion(t *testing.T) {
	ctx := context.Background()
	_, accRepo, _, _, _ := setupTestStore(t)

	// Create 2 accounts first
	acc1 := &domain.Account{
		ID:          "acc-1",
		Email:       "acc1@example.com",
		Status:      domain.AccountStatusActive,
		IsActive:    true,
		TokenExpiry: time.Now().Add(1 * time.Hour),
	}
	acc2 := &domain.Account{
		ID:          "acc-2",
		Email:       "acc2@example.com",
		Status:      domain.AccountStatusActive,
		IsActive:    false,
		TokenExpiry: time.Now().Add(1 * time.Hour),
	}
	if err := accRepo.Create(ctx, acc1); err != nil {
		t.Fatalf("failed to create acc1: %v", err)
	}
	if err := accRepo.Create(ctx, acc2); err != nil {
		t.Fatalf("failed to create acc2: %v", err)
	}

	// Verify default routing modes
	for _, cat := range []domain.ModelCategory{domain.CategoryClaudeGPT, domain.CategoryGemini, domain.CategoryGeminiArgon} {
		mode, err := accRepo.GetCategoryRoutingMode(ctx, cat)
		if err != nil {
			t.Fatalf("GetCategoryRoutingMode(%s) error: %v", cat, err)
		}
		if mode != domain.RoutingModePool {
			t.Fatalf("expected default mode 'pool' for %s, got %s", cat, mode)
		}
	}

	// Test SetCategoryRoutingMode to solo
	if err := accRepo.SetCategoryRoutingMode(ctx, domain.CategoryClaudeGPT, domain.RoutingModeSolo); err != nil {
		t.Fatalf("SetCategoryRoutingMode error: %v", err)
	}
	mode, err := accRepo.GetCategoryRoutingMode(ctx, domain.CategoryClaudeGPT)
	if err != nil {
		t.Fatalf("GetCategoryRoutingMode error: %v", err)
	}
	if mode != domain.RoutingModeSolo {
		t.Fatalf("expected mode 'solo', got %s", mode)
	}

	// Test GetAllCategoryRoutingModes
	allModes, err := accRepo.GetAllCategoryRoutingModes(ctx)
	if err != nil {
		t.Fatalf("GetAllCategoryRoutingModes error: %v", err)
	}
	if allModes[domain.CategoryClaudeGPT] != domain.RoutingModeSolo {
		t.Errorf("expected claude_gpt to be solo in allModes, got %s", allModes[domain.CategoryClaudeGPT])
	}
	if allModes[domain.CategoryGemini] != domain.RoutingModePool {
		t.Errorf("expected gemini to be pool in allModes, got %s", allModes[domain.CategoryGemini])
	}

	// Both accounts available for claude_gpt initially
	avail, err := accRepo.IsAccountAvailableForCategory(ctx, "acc-2", domain.CategoryClaudeGPT)
	if err != nil || !avail {
		t.Fatalf("expected acc-2 available for claude_gpt, got avail=%v, err=%v", avail, err)
	}

	// Disable claude_gpt on acc-2
	if err := accRepo.SetAccountCategoryDisabled(ctx, "acc-2", domain.CategoryClaudeGPT, true); err != nil {
		t.Fatalf("SetAccountCategoryDisabled error: %v", err)
	}

	// Verify acc-2 has disabled claude_gpt
	reloadedAcc2, err := accRepo.GetByID(ctx, "acc-2")
	if err != nil {
		t.Fatalf("GetByID error: %v", err)
	}
	if !reloadedAcc2.IsCategoryDisabled(domain.CategoryClaudeGPT) {
		t.Fatalf("expected acc-2 to have claude_gpt disabled")
	}
	if reloadedAcc2.IsCategoryDisabled(domain.CategoryGemini) {
		t.Fatalf("expected acc-2 to NOT have gemini disabled")
	}

	// IsAccountAvailableForCategory should now return false for acc-2 on claude_gpt
	avail, err = accRepo.IsAccountAvailableForCategory(ctx, "acc-2", domain.CategoryClaudeGPT)
	if err != nil || avail {
		t.Fatalf("expected acc-2 unavailable for claude_gpt, got avail=%v, err=%v", avail, err)
	}
	// But acc-2 is available for gemini
	avail, err = accRepo.IsAccountAvailableForCategory(ctx, "acc-2", domain.CategoryGemini)
	if err != nil || !avail {
		t.Fatalf("expected acc-2 available for gemini, got avail=%v, err=%v", avail, err)
	}

	// GetNextAvailableForCategory excluding acc-1 should find NO account for claude_gpt since acc-2 is excluded
	candidate, err := accRepo.GetNextAvailableForCategory(ctx, domain.CategoryClaudeGPT, "acc-1")
	if err == nil || candidate != nil {
		t.Fatalf("expected ErrNoAvailableAccount for claude_gpt, got candidate=%v, err=%v", candidate, err)
	}

	// GetNextAvailableForCategory excluding acc-1 should still find acc-2 for gemini
	candidate, err = accRepo.GetNextAvailableForCategory(ctx, domain.CategoryGemini, "acc-1")
	if err != nil || candidate == nil || candidate.ID != "acc-2" {
		t.Fatalf("expected acc-2 for gemini, got candidate=%v, err=%v", candidate, err)
	}

	// Re-enable claude_gpt on acc-2
	if err := accRepo.SetAccountCategoryDisabled(ctx, "acc-2", domain.CategoryClaudeGPT, false); err != nil {
		t.Fatalf("SetAccountCategoryDisabled false error: %v", err)
	}
	candidate, err = accRepo.GetNextAvailableForCategory(ctx, domain.CategoryClaudeGPT, "acc-1")
	if err != nil || candidate == nil || candidate.ID != "acc-2" {
		t.Fatalf("expected acc-2 to be available for claude_gpt after re-enabling, got candidate=%v, err=%v", candidate, err)
	}
}
