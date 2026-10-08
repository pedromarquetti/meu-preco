package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"meupreco/internal/domain"
)

func TestPreviewStoreLifecycle(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	u := seedUser(t, s)

	items := json.RawMessage(`[{"line":1,"status":"gtin_new"}]`)
	totals := json.RawMessage(`{"items":1,"total":"22.99"}`)
	p := &domain.Preview{
		UserID:         u.ID,
		AccessKey:      "35241152323688000144650020000002571115675901",
		PlanItems:      items,
		PlanTotals:     totals,
		Status:         "pending",
		IdempotencyKey: "idem-1",
		TTL:            time.Now().Add(15 * time.Minute),
	}
	if err := s.Previews().Create(ctx, p); err != nil {
		t.Fatalf("create preview: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("create did not set ID")
	}

	byID, err := s.Previews().GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.IdempotencyKey != "idem-1" || byID.Status != "pending" {
		t.Errorf("byID = %+v", byID)
	}
	if !jsonEqual(t, byID.PlanItems, items) {
		t.Errorf("plan_items = %s, want %s", byID.PlanItems, items)
	}
	if !jsonEqual(t, byID.PlanTotals, totals) {
		t.Errorf("plan_totals = %s, want %s", byID.PlanTotals, totals)
	}

	byKey, err := s.Previews().GetByIdempotencyKey(ctx, "idem-1")
	if err != nil {
		t.Fatalf("get by idempotency: %v", err)
	}
	if byKey.ID != p.ID {
		t.Errorf("byKey id = %d, want %d", byKey.ID, p.ID)
	}

	// First commit succeeds; a second confirm is a conflict.
	if err := s.Previews().MarkCommitted(ctx, p.ID); err != nil {
		t.Fatalf("mark committed: %v", err)
	}
	if err := s.Previews().MarkCommitted(ctx, p.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("double commit = %v, want ErrConflict", err)
	}
}

func TestPreviewStoreDuplicateIdempotencyKey(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	u := seedUser(t, s)
	mk := func() *domain.Preview {
		return &domain.Preview{
			UserID: u.ID, AccessKey: "35241152323688000144650020000002571115675901",
			PlanItems: json.RawMessage(`[]`), Status: "pending",
			IdempotencyKey: "idem-dup", TTL: time.Now().Add(time.Minute),
		}
	}
	if err := s.Previews().Create(ctx, mk()); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := s.Previews().Create(ctx, mk()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate idempotency key = %v, want ErrConflict", err)
	}
}
