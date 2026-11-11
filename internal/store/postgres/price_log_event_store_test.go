package store

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
	"meupreco/migrations"
)

// seedPriceLogForEvents creates the FK rows an event needs: a user (actor), a
// product (proposed target) and one uncertain price-log line.
func seedPriceLogForEvents(t *testing.T, s *Store) (*domain.PriceLog, *domain.Product, *domain.User) {
	t.Helper()
	ctx := context.Background()

	u := seedUser(t, s)
	sm := seedSupermarket(t, s)
	n := seedNfce(t, s, sm)

	p := &domain.Product{
		ProductKey: "07891000412855",
		GTIN:       stringPtr("7891000412855"),
		Name:       "LIMAO TAHITI KG",
		Unit:       "KG",
	}
	if _, err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("create product: %v", err)
	}

	l := &domain.PriceLog{
		NfceID:              n.ID,
		Line:                1,
		DescriptionSnapshot: "LIMAO TAHITI KG",
		Qty:                 decimal.NewFromFloat(1),
		Unit:                "KG",
		UnitPrice:           decimal.NewFromFloat(9.99),
		TotalPrice:          decimal.NewFromFloat(9.99),
		IdentityStatus:      "uid_uncertain",
		NeedsReview:         true,
	}
	if err := s.PriceLogs().Create(ctx, l); err != nil {
		t.Fatalf("create price log: %v", err)
	}
	return l, p, u
}

// TestPriceLogEventStoreCreate proves nullable product ids persist, the action
// CHECK accepts the three expected actions, and the database fills in the id
// and timestamp.
func TestPriceLogEventStoreCreate(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	l, p, u := seedPriceLogForEvents(t, s)

	assign := &domain.PriceLogEvent{
		PriceLogID:  l.ID,
		ToProductID: &p.ID,
		UserID:      u.ID,
		Action:      "assign",
	}
	if err := s.Events().Create(ctx, assign); err != nil {
		t.Fatalf("create assign event: %v", err)
	}
	if assign.ID == 0 {
		t.Fatal("create did not set the event ID")
	}
	if assign.Timestamp.IsZero() {
		t.Fatal("create did not return the database timestamp")
	}

	approve := &domain.PriceLogEvent{
		PriceLogID:    l.ID,
		FromProductID: &p.ID,
		ToProductID:   &p.ID,
		UserID:        u.ID,
		Action:        "approve",
	}
	if err := s.Events().Create(ctx, approve); err != nil {
		t.Fatalf("create approve event: %v", err)
	}

	reject := &domain.PriceLogEvent{
		PriceLogID:    l.ID,
		FromProductID: &p.ID,
		ToProductID:   &p.ID,
		UserID:        u.ID,
		Action:        "reject",
	}
	if err := s.Events().Create(ctx, reject); err != nil {
		t.Fatalf("create reject event: %v", err)
	}

	// The assign row keeps from_product_id NULL and to_product_id set.
	var fromID *int64
	var toID *int64
	err := s.pool.QueryRow(ctx,
		`SELECT from_product_id, to_product_id FROM price_log_event WHERE id = $1`,
		assign.ID,
	).Scan(&fromID, &toID)
	if err != nil {
		t.Fatalf("read assign event: %v", err)
	}
	if fromID != nil {
		t.Errorf("assign.from_product_id = %v, want NULL", *fromID)
	}
	if toID == nil || *toID != p.ID {
		t.Errorf("assign.to_product_id = %v, want %d", toID, p.ID)
	}
}

// TestPriceLogEventStoreRejectsInvalidAction proves the table CHECK is the
// validation boundary for unexpected actions.
func TestPriceLogEventStoreRejectsInvalidAction(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	l, _, u := seedPriceLogForEvents(t, s)

	e := &domain.PriceLogEvent{
		PriceLogID: l.ID,
		UserID:     u.ID,
		Action:     "delete",
	}
	if err := s.Events().Create(ctx, e); err == nil {
		t.Fatal("invalid action was accepted, want a CHECK violation")
	}

	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM price_log_event WHERE price_log_id = $1`, l.ID,
	).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 0 {
		t.Fatalf("invalid action left %d event row(s), want 0", n)
	}
}

// TestPriceLogEventStoreHistoryIsAppendOnly proves events stay ordered and a
// later decision never rewrites or removes an earlier one.
func TestPriceLogEventStoreHistoryIsAppendOnly(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	l, p, u := seedPriceLogForEvents(t, s)

	want := []string{"assign", "reject", "approve"}
	for _, action := range want {
		e := &domain.PriceLogEvent{
			PriceLogID:    l.ID,
			FromProductID: &p.ID,
			ToProductID:   &p.ID,
			UserID:        u.ID,
			Action:        action,
		}
		if action == "assign" {
			e.FromProductID = nil
		}
		if err := s.Events().Create(ctx, e); err != nil {
			t.Fatalf("create %s event: %v", action, err)
		}
	}

	rows, err := s.pool.Query(ctx,
		`SELECT action FROM price_log_event
		 WHERE price_log_id = $1
		 ORDER BY timestamp, id`, l.ID)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatalf("scan action: %v", err)
		}
		got = append(got, action)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event order = %v, want %v", got, want)
		}
	}
}

// TestPriceLogEventStoreRollback proves a price-log mutation and its event are
// atomic: when the transaction aborts, neither row survives.
func TestPriceLogEventStoreRollback(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	l, p, u := seedPriceLogForEvents(t, s)

	err := s.Tx(ctx, func(tx domain.Stores) error {
		mutated := *l
		mutated.ID = 0
		mutated.Line = 2
		mutated.ProductID = &p.ID
		mutated.NeedsReview = false
		if err := tx.PriceLogs().Create(ctx, &mutated); err != nil {
			return err
		}
		if err := tx.Events().Create(ctx, &domain.PriceLogEvent{
			PriceLogID:    mutated.ID,
			FromProductID: &p.ID,
			ToProductID:   &p.ID,
			UserID:        u.ID,
			Action:        "approve",
		}); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("tx returned nil, want the injected error")
	}

	if _, err := s.PriceLogs().GetByNfceLine(ctx, l.NfceID, 2); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("price log survived rollback: %v (want ErrNotFound)", err)
	}

	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM price_log_event WHERE price_log_id <> $1`, l.ID,
	).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 0 {
		t.Errorf("event survived rollback: %d row(s)", n)
	}
}

// TestPriceLogEventBackfillIdempotent proves the legacy-assignment migration
// quarantines the row, records exactly one assign event, and is safe to re-run.
func TestPriceLogEventBackfillIdempotent(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	l, p, u := seedPriceLogForEvents(t, s)
	note := "assign"
	if _, err := s.pool.Exec(ctx,
		`UPDATE price_logs
		    SET product_id = $2, needs_review = false, resolved_by = $3, resolution_note = $4
		  WHERE id = $1`, l.ID, p.ID, u.ID, note); err != nil {
		t.Fatalf("seed legacy assignment: %v", err)
	}

	backfill, err := migrations.FS.ReadFile("000015_price_log_event_backfill.up.sql")
	if err != nil {
		t.Fatalf("read backfill migration: %v", err)
	}
	for run := 1; run <= 2; run++ {
		if _, err := s.pool.Exec(ctx, string(backfill)); err != nil {
			t.Fatalf("backfill run %d: %v", run, err)
		}
	}

	var needsReview bool
	if err := s.pool.QueryRow(ctx,
		`SELECT needs_review FROM price_logs WHERE id = $1`, l.ID).Scan(&needsReview); err != nil {
		t.Fatalf("read price log: %v", err)
	}
	if !needsReview {
		t.Error("legacy assignment was not quarantined (needs_review=false)")
	}

	rows, err := s.pool.Query(ctx,
		`SELECT from_product_id, to_product_id, user_id, action
		   FROM price_log_event WHERE price_log_id = $1`, l.ID)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var from, to *int64
		var userID int64
		var action string
		if err := rows.Scan(&from, &to, &userID, &action); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		count++
		if from != nil {
			t.Errorf("backfilled from_product_id = %v, want NULL", *from)
		}
		if to == nil || *to != p.ID {
			t.Errorf("backfilled to_product_id = %v, want %d", to, p.ID)
		}
		if userID != u.ID || action != "assign" {
			t.Errorf("backfilled event = user:%d action:%s, want %d/assign", userID, action, u.ID)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if count != 1 {
		t.Errorf("backfill created %d events across two runs, want 1", count)
	}
}
