package store

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

func TestPriceLogStoreLifecycle(t *testing.T) {
	s := mustStore(t)
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
	s := mustStore(t)
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
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	if _, err := s.PriceLogs().GetByNfceLine(ctx, 1, 99); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
