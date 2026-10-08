package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

// seedUser creates a user for FK-dependent tests.
func seedUser(t *testing.T, s *Store) *domain.User {
	t.Helper()
	u := &domain.User{Email: "seed@example.com", PasswordHash: "h", Role: "user"}
	if err := s.Users().Create(context.Background(), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

// seedSupermarket creates a supermarket for FK-dependent tests.
func seedSupermarket(t *testing.T, s *Store) *domain.Supermarket {
	t.Helper()
	sm := &domain.Supermarket{
		CNPJ:      "12345678000199",
		LegalName: "MERCADO BOM PRECO LTDA",
	}
	if err := s.Supermarkets().Upsert(context.Background(), sm); err != nil {
		t.Fatalf("seed supermarket: %v", err)
	}
	return sm
}

// seedNfce creates a full Nfce owned by the given supermarket.
func seedNfce(t *testing.T, s *Store, sm *domain.Supermarket) *domain.Nfce {
	t.Helper()
	issued := time.Date(2024, 11, 15, 8, 52, 53, 0, time.UTC)
	n := &domain.Nfce{
		AccessKey:     "35241152323688000144650020000002571115675901",
		Supermarket:   *sm,
		Number:        "257",
		Series:        "11",
		IssuedAt:      &issued,
		Protocol:      "135241234567890",
		Environment:   "Produção",
		PaymentMethod: "Cartão de Crédito",
		Total:         decimal.NewFromFloat(59.06),
		Discount:      decimal.NewFromFloat(2.50),
		PaidValue:     decimal.NewFromFloat(56.56),
		COO:           "123456",
		PayloadRaw:    "<dummy danfe>",
	}
	if err := s.Nfces().Create(context.Background(), n); err != nil {
		t.Fatalf("seed nfce: %v", err)
	}
	return n
}

func TestNfceStoreLifecycle(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	sm := seedSupermarket(t, s)
	n := seedNfce(t, s, sm)
	if n.ID == 0 {
		t.Fatal("create did not set ID")
	}

	byID, err := s.Nfces().GetByID(ctx, n.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.AccessKey != n.AccessKey || byID.Supermarket.CNPJ != sm.CNPJ {
		t.Errorf("byID = %+v", byID)
	}
	if !byID.Total.Equal(n.Total) || !byID.PaidValue.Equal(n.PaidValue) {
		t.Errorf("totals = %v/%v", byID.Total, byID.PaidValue)
	}

	byKey, err := s.Nfces().GetByAccessKey(ctx, n.AccessKey)
	if err != nil {
		t.Fatalf("get by access key: %v", err)
	}
	if byKey.ID != n.ID {
		t.Errorf("byKey id = %d, want %d", byKey.ID, n.ID)
	}
}

func TestNfceStoreDuplicateAccessKey(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	sm := seedSupermarket(t, s)
	n1 := seedNfce(t, s, sm)

	dup := *n1
	dup.ID = 0
	err := s.Nfces().Create(ctx, &dup)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate access_key = %v, want ErrConflict", err)
	}
}

func TestNfceStoreNotFound(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	if _, err := s.Nfces().GetByAccessKey(ctx, "99999999999999999999999999999999999999999999"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
