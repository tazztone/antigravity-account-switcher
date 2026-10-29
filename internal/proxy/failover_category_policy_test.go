package proxy

import (
	"context"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

func TestFailover_SoloMode_BlocksCrossAccountRotation(t *testing.T) {
	ctx := context.Background()
	accRepo := newMockAccountRepo()
	eventRepo := &mockEventRepo{}
	broadcaster := NewBroadcaster(10)

	acc1 := &domain.Account{
		ID:        "acc-1",
		Email:     "acc1@example.com",
		Status:    domain.AccountStatusActive,
		IsActive:  true,
		UpdatedAt: time.Now().UTC(),
	}
	acc2 := &domain.Account{
		ID:        "acc-2",
		Email:     "acc2@example.com",
		Status:    domain.AccountStatusActive,
		IsActive:  false,
		UpdatedAt: time.Now().UTC(),
	}
	accRepo.addAccount(acc1)
	accRepo.addAccount(acc2)

	// Set claude_gpt to Solo mode
	_ = accRepo.SetCategoryRoutingMode(ctx, domain.CategoryClaudeGPT, domain.RoutingModeSolo)

	engine := NewFailoverEngine(accRepo, broadcaster, eventRepo)

	// Direct rotation for claude_gpt should be blocked
	next, err := engine.RotateAccountForCategory(ctx, acc1, domain.CategoryClaudeGPT)
	if err != domain.ErrNoAvailableAccount {
		t.Fatalf("expected ErrNoAvailableAccount for solo mode, got err=%v, next=%v", err, next)
	}

	// Verify EventTypeQuotaExhausted with reason category_locked_to_account was recorded
	var foundLockedEvent bool
	for _, ev := range eventRepo.events {
		if ev.Type == domain.EventTypeQuotaExhausted && ev.Details["reason"] == "category_locked_to_account" {
			foundLockedEvent = true
			break
		}
	}
	if !foundLockedEvent {
		t.Fatalf("expected EventTypeQuotaExhausted with reason category_locked_to_account, got events: %+v", eventRepo.events)
	}
}

func TestFailover_SoloMode_AllowsIntraAccountFallback(t *testing.T) {
	ctx := context.Background()
	accRepo := newMockAccountRepo()
	eventRepo := &mockEventRepo{}
	broadcaster := NewBroadcaster(10)

	acc1 := &domain.Account{
		ID:        "acc-1",
		Email:     "acc1@example.com",
		Status:    domain.AccountStatusActive,
		IsActive:  true,
		UpdatedAt: time.Now().UTC(),
	}
	acc2 := &domain.Account{
		ID:        "acc-2",
		Email:     "acc2@example.com",
		Status:    domain.AccountStatusActive,
		IsActive:  false,
		UpdatedAt: time.Now().UTC(),
	}
	accRepo.addAccount(acc1)
	accRepo.addAccount(acc2)

	// Set claude_gpt to Solo mode
	_ = accRepo.SetCategoryRoutingMode(ctx, domain.CategoryClaudeGPT, domain.RoutingModeSolo)

	// Configure intra-account model fallback (claude-3-7-sonnet -> claude-3-5-haiku)
	engine := NewFailoverEngine(
		accRepo,
		broadcaster,
		eventRepo,
		WithModelFallback("claude-3-7-sonnet", "claude-3-5-haiku", true),
	)

	// Exhaustion on primary model: should trigger intra-account fallback to secondary on same account!
	action, targetModel, targetAcc, err := engine.HandleExhaustion(ctx, acc1, "claude-3-7-sonnet")
	if err != nil {
		t.Fatalf("HandleExhaustion primary error: %v", err)
	}
	if action != ActionFallbackSecondary {
		t.Fatalf("expected ActionFallbackSecondary, got %v", action)
	}
	if targetModel != "claude-3-5-haiku" {
		t.Fatalf("expected targetModel claude-3-5-haiku, got %s", targetModel)
	}
	if targetAcc.ID != "acc-1" {
		t.Fatalf("expected targetAcc to remain acc-1, got %s", targetAcc.ID)
	}

	// Now exhaust the secondary model: since claude_gpt is Solo, cross-account rotation must be blocked!
	_, _, targetAcc, err = engine.HandleExhaustion(ctx, acc1, "claude-3-5-haiku")
	if err != domain.ErrNoAvailableAccount {
		t.Fatalf("expected ErrNoAvailableAccount when secondary exhausted in solo mode, got err=%v", err)
	}
	if targetAcc != nil {
		t.Fatalf("expected nil targetAcc on blocked rotation, got %v", targetAcc)
	}
}

func TestFailover_PoolMode_SkipsExcludedAccounts(t *testing.T) {
	ctx := context.Background()
	accRepo := newMockAccountRepo()
	eventRepo := &mockEventRepo{}
	broadcaster := NewBroadcaster(10)

	acc1 := &domain.Account{
		ID:        "acc-1",
		Email:     "acc1@example.com",
		Status:    domain.AccountStatusActive,
		IsActive:  true,
		UpdatedAt: time.Now().UTC(),
	}
	acc2 := &domain.Account{
		ID:                 "acc-2",
		Email:              "acc2@example.com",
		Status:             domain.AccountStatusActive,
		IsActive:           false,
		DisabledCategories: []string{"claude_gpt"}, // acc-2 opts out of claude_gpt
		UpdatedAt:          time.Now().UTC().Add(1 * time.Second),
	}
	acc3 := &domain.Account{
		ID:        "acc-3",
		Email:     "acc3@example.com",
		Status:    domain.AccountStatusActive,
		IsActive:  false,
		UpdatedAt: time.Now().UTC().Add(2 * time.Second),
	}
	accRepo.addAccount(acc1)
	accRepo.addAccount(acc2)
	accRepo.addAccount(acc3)

	_ = accRepo.SetCategoryRoutingMode(ctx, domain.CategoryClaudeGPT, domain.RoutingModePool)

	engine := NewFailoverEngine(accRepo, broadcaster, eventRepo)

	// Failover from acc-1 on claude_gpt should bypass acc-2 (since claude_gpt is disabled on acc-2)
	// and rotate to acc-3
	next, err := engine.RotateAccountForCategory(ctx, acc1, domain.CategoryClaudeGPT)
	if err != nil {
		t.Fatalf("RotateAccountForCategory error: %v", err)
	}
	if next.ID != "acc-3" {
		t.Fatalf("expected rotation to skip acc-2 and pick acc-3, got %s", next.ID)
	}
}
