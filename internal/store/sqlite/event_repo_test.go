package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/store/sqlite"
)

func TestEventRepository_RecordAndListRecent(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open failed: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewEventRepository(db)
	ctx := context.Background()

	// System event with empty AccountID must succeed without FK constraint failure.
	sysEvent := &domain.ProxyEvent{
		Type:      domain.EventTypeAccountSwitched,
		AccountID: "",
		Message:   "system-level failover",
		Details:   map[string]any{"reason": "exhausted"},
	}
	if err := repo.Record(ctx, sysEvent); err != nil {
		t.Fatalf("Record(empty AccountID) failed: %v", err)
	}
	if sysEvent.ID == 0 {
		t.Error("expected non-zero ID after Record")
	}

	// Non-UTC timestamp should be normalized to UTC.
	loc := time.FixedZone("UTC+2", 2*3600)
	accRepo := sqlite.NewAccountRepository(db)
	acc := &domain.Account{
		ID:           "acc-evt-1",
		Email:        "evt@example.com",
		RefreshToken: "rt",
		Status:       domain.AccountStatusActive,
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("Create account failed: %v", err)
	}

	accEvent := &domain.ProxyEvent{
		Type:      domain.EventTypeAccountSwitched,
		AccountID: acc.ID,
		Message:   "switched account",
		Timestamp: time.Date(2026, 6, 1, 12, 0, 0, 0, loc),
	}
	if err := repo.Record(ctx, accEvent); err != nil {
		t.Fatalf("Record(accEvent) failed: %v", err)
	}

	events, err := repo.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// ListRecent with limit <= 0 should clamp cleanly.
	clamped, err := repo.ListRecent(ctx, 0)
	if err != nil {
		t.Fatalf("ListRecent(0) failed: %v", err)
	}
	if len(clamped) != 2 {
		t.Errorf("expected 2 events with clamped limit, got %d", len(clamped))
	}
}
