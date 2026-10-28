package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"meupreco/internal/domain"
)

// TestStoreTxCommit verifies Stores.Tx commits a group of writes atomically:
// everything fn wrote is visible once it returns nil.
func TestStoreTxCommit(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "TX MERCADO"}
	err := s.Tx(ctx, func(tx domain.Stores) error {
		return tx.Supermarkets().Upsert(ctx, sm)
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if sm.ID == 0 {
		t.Fatal("upsert did not set id inside the tx")
	}

	got, err := s.Supermarkets().GetByCNPJ(ctx, sm.CNPJ)
	if err != nil {
		t.Fatalf("row not visible after commit: %v", err)
	}
	if got.ID != sm.ID || got.LegalName != "TX MERCADO" {
		t.Errorf("committed row = %+v", got)
	}
}

// TestStoreTxRollback verifies that a failing fn rolls every write back.
func TestStoreTxRollback(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "ROLLBACK"}
	err := s.Tx(ctx, func(tx domain.Stores) error {
		if err := tx.Supermarkets().Upsert(ctx, sm); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("tx returned nil, want the injected error")
	}

	if _, err := s.Supermarkets().GetByCNPJ(ctx, sm.CNPJ); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("row survived rollback: %v (want ErrNotFound)", err)
	}
}

// TestStoreTxNestedRejected guards against double-begin on a tx-bound store.
func TestStoreTxNestedRejected(t *testing.T) {
	s := testStore
	ctx := context.Background()

	err := s.Tx(ctx, func(tx domain.Stores) error {
		return tx.Tx(ctx, func(domain.Stores) error { return nil })
	})
	if err == nil {
		t.Fatal("nested Tx returned nil, want an error")
	}
}

// TestStoreTxSharedPreviews exercises the txStores previews repo (create +
// mark committed inside one transaction).
func TestStoreTxSharedPreviews(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	u := seedUser(t, s)
	var previewID int64
	err := s.Tx(ctx, func(tx domain.Stores) error {
		p := &domain.Preview{
			UserID: u.ID, AccessKey: "35241152323688000144650020000002571115675901",
			PlanItems: json.RawMessage(`[{"line":1}]`),
			Status:    "pending", TTL: time.Now().Add(time.Minute),
		}
		if err := tx.Previews().Create(ctx, p); err != nil {
			return err
		}
		previewID = p.ID
		return tx.Previews().MarkCommitted(ctx, p.ID)
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	got, err := s.Previews().GetByID(ctx, previewID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "committed" {
		t.Errorf("status = %q, want committed", got.Status)
	}
}

// TestStoreTxCommittedFreesPendingSlot verifies MarkCommitted inside a tx
// releases the (user, access_key) pending slot the partial unique index guards.
func TestStoreTxCommittedFreesPendingSlot(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	u := seedUser(t, s)
	err := s.Tx(ctx, func(tx domain.Stores) error {
		p := &domain.Preview{
			UserID: u.ID, AccessKey: "35241152323688000144650020000002571115675901",
			PlanItems: json.RawMessage(`[{"line":1}]`),
			Status:    "pending", TTL: time.Now().Add(time.Minute),
		}
		if err := tx.Previews().Create(ctx, p); err != nil {
			return err
		}
		return tx.Previews().MarkCommitted(ctx, p.ID)
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	// No pending row is left for (user, access_key).
	if _, err := s.Previews().GetPendingByUserAccessKey(ctx, u.ID, "35241152323688000144650020000002571115675901"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("committed preview still pending = %v", err)
	}
	// And a fresh draft is now allowed.
	fresh, err := createPreview(t, s, u.ID, "35241152323688000144650020000002571115675901", "pending")
	if err != nil {
		t.Fatalf("re-draft after committed-in-tx: %v", err)
	}
	if fresh == 0 {
		t.Fatal("fresh draft id is zero")
	}
}
