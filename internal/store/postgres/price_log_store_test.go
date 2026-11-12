package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

func TestPriceLogStoreLifecycle(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	sm := seedSupermarket(t, s)
	n := seedNfce(t, s, sm)
	p := &domain.Product{ProductKey: "07896008038717", GTIN: stringPtr("7896008038717"),
		Name: "ARROZ BRANCO TIPO 1 5KG", Unit: "UN"}
	if _, err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("create product: %v", err)
	}

	ppu := decimal.NewFromFloat(22.99)
	log := &domain.PriceLog{
		NfceID:              n.ID,
		ProductID:           &p.ID,
		Line:                1,
		DescriptionSnapshot: "ARROZ BRANCO TIPO 1 5KG",
		Qty:                 decimal.NewFromFloat(1),
		Unit:                "UN",
		NormalizedUnit:      stringPtr("UN"),
		PricePerUnit:        &ppu,
		UnitPrice:           decimal.NewFromFloat(22.99),
		TotalPrice:          decimal.NewFromFloat(22.99),
		IdentityStatus:      "gtin_new",
	}
	if err := s.PriceLogs().Create(ctx, log); err != nil {
		t.Fatalf("create price log: %v", err)
	}
	if log.ID == 0 {
		t.Fatal("create did not set ID")
	}

	byID, err := s.PriceLogs().GetByID(ctx, log.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.NfceID != n.ID || byID.ProductID == nil || *byID.ProductID != p.ID {
		t.Errorf("byID = %+v", byID)
	}
	if !byID.Qty.Equal(log.Qty) || !byID.UnitPrice.Equal(log.UnitPrice) {
		t.Errorf("money = %v / %v", byID.Qty, byID.UnitPrice)
	}
	if byID.PricePerUnit == nil {
		t.Error("price_per_unit nil")
	}

	byLine, err := s.PriceLogs().GetByNfceLine(ctx, n.ID, 1)
	if err != nil {
		t.Fatalf("get by nfce line: %v", err)
	}
	if byLine.ID != log.ID {
		t.Errorf("byLine id = %d, want %d", byLine.ID, log.ID)
	}
}

func TestPriceLogStoreDuplicateLine(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	sm := seedSupermarket(t, s)
	n := seedNfce(t, s, sm)

	base := &domain.PriceLog{
		NfceID: n.ID, Line: 1, DescriptionSnapshot: "X", Qty: decimal.NewFromFloat(1),
		Unit: "UN", UnitPrice: decimal.NewFromFloat(1), TotalPrice: decimal.NewFromFloat(1),
		IdentityStatus: "gtin_known",
	}
	if err := s.PriceLogs().Create(ctx, base); err != nil {
		t.Fatalf("first create: %v", err)
	}

	dup := *base
	dup.ID = 0
	err := s.PriceLogs().Create(ctx, &dup)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate (nfce_id, line) = %v, want ErrConflict", err)
	}
}

func TestPriceLogStoreNotFound(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	if _, err := s.PriceLogs().GetByNfceLine(ctx, 1, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestNeedsReviewProjectsProposal checks the admin queue includes both an
// unresolved row (null product) and a manual assignment, exposing the proposed
// product and its resolver through the join projection.
func TestNeedsReviewProjectsProposal(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	u := seedUser(t, s)
	sm := seedSupermarket(t, s)
	n := seedNfce(t, s, sm)
	product := &domain.Product{
		ProductKey: "07891000412855",
		GTIN:       stringPtr("07891000412855"),
		Name:       "LIMAO TAHTI",
		Unit:       "KG",
	}
	if _, err := s.Products().Create(ctx, product); err != nil {
		t.Fatalf("create product: %v", err)
	}

	assignNote := "assign"
	assigned := &domain.PriceLog{
		NfceID: n.ID, ProductID: &product.ID, Line: 1,
		DescriptionSnapshot: "LIMAO TAHITI KG", StoreCode: "000222",
		Qty: decimal.NewFromFloat(1), Unit: "KG",
		UnitPrice: decimal.NewFromFloat(5.99), TotalPrice: decimal.NewFromFloat(5.99),
		IdentityStatus: "uid_uncertain", NeedsReview: true,
		ResolvedBy: &u.ID, ResolutionNote: &assignNote,
	}
	if err := s.PriceLogs().Create(ctx, assigned); err != nil {
		t.Fatalf("create assigned log: %v", err)
	}
	reviewNote := "needs_review"
	unresolved := &domain.PriceLog{
		NfceID: n.ID, Line: 2,
		DescriptionSnapshot: "FRUTA AVULSA", StoreCode: "000333",
		Qty: decimal.NewFromFloat(1), Unit: "KG",
		UnitPrice: decimal.NewFromFloat(1), TotalPrice: decimal.NewFromFloat(1),
		IdentityStatus: "uid_uncertain", NeedsReview: true,
		ResolutionNote: &reviewNote,
	}
	if err := s.PriceLogs().Create(ctx, unresolved); err != nil {
		t.Fatalf("create unresolved log: %v", err)
	}

	items, total, err := s.PriceLogs().NeedsReview(ctx, 20, 0)
	if err != nil {
		t.Fatalf("needs review: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("queue = %d rows (total %d), want 2", len(items), total)
	}

	byID := map[int64]domain.ReviewItem{}
	for _, it := range items {
		byID[it.PriceLogID] = it
	}

	got := byID[assigned.ID]
	if got.ProductID == nil || *got.ProductID != product.ID {
		t.Errorf("product_id = %v, want %d", got.ProductID, product.ID)
	}
	if got.ProductKey == nil || *got.ProductKey != product.ProductKey ||
		got.ProductGTIN == nil || *got.ProductGTIN != *product.GTIN ||
		got.ProductName == nil || *got.ProductName != product.Name ||
		got.ProductUnit == nil || *got.ProductUnit != product.Unit {
		t.Errorf("proposed product projection = %+v", got)
	}
	if got.ResolvedBy == nil || *got.ResolvedBy != u.ID {
		t.Errorf("resolved_by = %v, want %d", got.ResolvedBy, u.ID)
	}
	if got.ResolutionNote == nil || *got.ResolutionNote != assignNote {
		t.Errorf("resolution_note = %v, want assign", got.ResolutionNote)
	}

	plain := byID[unresolved.ID]
	if plain.ProductID != nil || plain.ProductName != nil || plain.ResolvedBy != nil {
		t.Errorf("unresolved projection = %+v, want null proposal", plain)
	}
	if plain.ResolutionNote == nil || *plain.ResolutionNote != reviewNote {
		t.Errorf("unresolved note = %v, want needs_review", plain.ResolutionNote)
	}

	// Rows without events keep a nil latest action (legacy/unresolved state).
	if got.LastReviewAction != nil || plain.LastReviewAction != nil {
		t.Errorf("latest action without events = %v/%v, want nil", got.LastReviewAction, plain.LastReviewAction)
	}

	// The queue projects the latest event for the row: assign then reject.
	ts := time.Date(2024, 11, 2, 10, 0, 0, 0, time.UTC)
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO price_log_event (price_log_id, from_product_id, to_product_id, user_id, action, timestamp)
		 VALUES ($1, $2, $3, $4, 'assign', $5)`,
		assigned.ID, product.ID, product.ID, u.ID, ts); err != nil {
		t.Fatalf("insert assign event: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO price_log_event (price_log_id, from_product_id, to_product_id, user_id, action, timestamp)
		 VALUES ($1, $2, $3, $4, 'reject', $5)`,
		assigned.ID, product.ID, product.ID, u.ID, ts.Add(time.Minute)); err != nil {
		t.Fatalf("insert reject event: %v", err)
	}

	items, _, err = s.PriceLogs().NeedsReview(ctx, 20, 0)
	if err != nil {
		t.Fatalf("needs review after events: %v", err)
	}
	for _, it := range items {
		if it.PriceLogID == assigned.ID {
			if it.LastReviewAction == nil || *it.LastReviewAction != "reject" {
				t.Errorf("latest action = %v, want reject", it.LastReviewAction)
			}
		}
	}

	// Equal timestamps resolve by event id: the later insert wins.
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO price_log_event (price_log_id, from_product_id, to_product_id, user_id, action, timestamp)
		 VALUES ($1, $2, $3, $4, 'reject', $5)`,
		unresolved.ID, product.ID, product.ID, u.ID, ts); err != nil {
		t.Fatalf("insert tie reject event: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO price_log_event (price_log_id, from_product_id, to_product_id, user_id, action, timestamp)
		 VALUES ($1, $2, $3, $4, 'assign', $5)`,
		unresolved.ID, product.ID, product.ID, u.ID, ts); err != nil {
		t.Fatalf("insert tie assign event: %v", err)
	}
	items, _, err = s.PriceLogs().NeedsReview(ctx, 20, 0)
	if err != nil {
		t.Fatalf("needs review after tie: %v", err)
	}
	for _, it := range items {
		if it.PriceLogID == unresolved.ID {
			if it.LastReviewAction == nil || *it.LastReviewAction != "assign" {
				t.Errorf("tie-break latest action = %v, want assign (higher id)", it.LastReviewAction)
			}
		}
	}
}
