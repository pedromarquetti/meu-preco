package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"meupreco/internal/domain"
)

const testAccessKey = "35241152323688000144650020000002571115675901"

func TestPreviewStoreLifecycle(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	u := seedUser(t, s)

	items := json.RawMessage(`[{"line":1,"status":"gtin_new"}]`)
	totals := json.RawMessage(`{"items":1,"total":"22.99"}`)
	p := &domain.Preview{
		UserID:     u.ID,
		AccessKey:  testAccessKey,
		PlanItems:  items,
		PlanTotals: totals,
		Status:     "pending",
		TTL:        time.Now().Add(15 * time.Minute),
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
	if byID.Status != "pending" {
		t.Errorf("byID = %+v", byID)
	}
	if !jsonEqual(t, byID.PlanItems, items) {
		t.Errorf("plan_items = %s, want %s", byID.PlanItems, items)
	}
	if !jsonEqual(t, byID.PlanTotals, totals) {
		t.Errorf("plan_totals = %s, want %s", byID.PlanTotals, totals)
	}

	byKey, err := s.Previews().GetPendingByUserAccessKey(ctx, u.ID, testAccessKey)
	if err != nil {
		t.Fatalf("get pending by (user, access_key): %v", err)
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

	// The partial unique index covers only pending rows: a fresh draft of the
	// same receipt is allowed after the committed one.
	id2, err := createPreview(t, s, u.ID, testAccessKey, "pending")
	if err != nil {
		t.Fatalf("re-draft after commit: %v", err)
	}
	if id2 == p.ID {
		t.Errorf("re-draft reused id %d", p.ID)
	}
}

func createPreview(t *testing.T, s *Store, userID int64, accessKey, status string) (int64, error) {
	t.Helper()
	p := &domain.Preview{
		UserID:     userID,
		AccessKey:  accessKey,
		PlanItems:  json.RawMessage(`[]`),
		PlanTotals: json.RawMessage(`{}`),
		Status:     status,
		TTL:        time.Now().Add(time.Minute),
	}
	if err := s.Previews().Create(context.Background(), p); err != nil {
		return 0, err
	}
	return p.ID, nil
}

func TestPreviewStoreUserAccessKeyUniqueWhilePending(t *testing.T) {
	s := testStore
	truncate(t)

	u := seedUser(t, s)
	other := &domain.User{Name: "Other", Email: "other@example.com", PasswordHash: "h", Role: "user"}
	if err := s.Users().Create(context.Background(), other); err != nil {
		t.Fatalf("seed second user: %v", err)
	}

	if _, err := createPreview(t, s, u.ID, testAccessKey, "pending"); err != nil {
		t.Fatalf("first pending: %v", err)
	}
	if _, err := createPreview(t, s, u.ID, testAccessKey, "pending"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate pending (user, access_key) = %v, want ErrConflict", err)
	}
	// The index is owner-scoped: another user may draft the same receipt.
	if _, err := createPreview(t, s, other.ID, testAccessKey, "pending"); err != nil {
		t.Fatalf("second user pending = %v, want no conflict", err)
	}
	// Committed rows do not reserve the slot.
	committed, err := createPreview(t, s, u.ID, "35241211222333000181650010000000911449128550", "committed")
	if err != nil {
		t.Fatalf("committed create: %v", err)
	}
	if _, err := createPreview(t, s, u.ID, "35241211222333000181650010000000911449128550", "pending"); err != nil {
		t.Fatalf("pending after committed = %v, want no conflict", err)
	}
	_ = committed
}

func TestPreviewStorePurgeExpired(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	u := seedUser(t, s)

	fresh := &domain.Preview{UserID: u.ID, AccessKey: "35241152323688000144650020000002571115675902",
		PlanItems: json.RawMessage(`[]`), Status: "pending", TTL: time.Now().Add(15 * time.Minute)}
	stale := &domain.Preview{UserID: u.ID, AccessKey: "35241152323688000144650020000002571115675903",
		PlanItems: json.RawMessage(`[]`), Status: "pending", TTL: time.Now().Add(-time.Minute)}
	committed := &domain.Preview{UserID: u.ID, AccessKey: "35241152323688000144650020000002571115675904",
		PlanItems: json.RawMessage(`[]`), Status: "committed", TTL: time.Now().Add(-2 * time.Hour)}

	for _, p := range []*domain.Preview{fresh, stale, committed} {
		if err := s.Previews().Create(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	n, err := s.Previews().PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 2 {
		t.Errorf("purged = %d, want 2 (stale + committed past ttl)", n)
	}

	if _, err := s.Previews().GetPendingByUserAccessKey(ctx, u.ID, "35241152323688000144650020000002571115675902"); err != nil {
		t.Errorf("fresh preview removed: %v", err)
	}
	if _, err := s.Previews().GetPendingByUserAccessKey(ctx, u.ID, "35241152323688000144650020000002571115675903"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("stale preview survived: %v", err)
	}
	if _, err := s.Previews().GetPendingByUserAccessKey(ctx, u.ID, "35241152323688000144650020000002571115675904"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("committed preview survived purge: %v", err)
	}
}
