package store

import (
	"context"
	"errors"
	"testing"

	"meupreco/internal/domain"
)

func TestSupermarketUpsertIdempotent(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	sm := &domain.Supermarket{
		CNPJ:      "12345678000199",
		LegalName: "MERCADO BOM PRECO LTDA",
		Address:   "Rua A, 10",
	}
	if err := s.Supermarkets().Upsert(ctx, sm); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	firstID := sm.ID
	if firstID == 0 {
		t.Fatal("upsert did not set ID")
	}

	// Same CNPJ, changed name/address: the same row is updated, not duplicated.
	sm.LegalName = "MERCADO BOM PRECO NOVO NOME"
	sm.Address = "Rua B, 20"
	if err := s.Supermarkets().Upsert(ctx, sm); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if sm.ID != firstID {
		t.Errorf("re-upsert created a new row: id %d -> %d", firstID, sm.ID)
	}

	got, err := s.Supermarkets().GetByCNPJ(ctx, "12345678000199")
	if err != nil {
		t.Fatalf("get by cnpj: %v", err)
	}
	if got.ID != firstID || got.LegalName != "MERCADO BOM PRECO NOVO NOME" {
		t.Errorf("got = %+v", got)
	}

	byID, err := s.Supermarkets().GetByID(ctx, firstID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.Address != "Rua B, 20" {
		t.Errorf("address = %q", byID.Address)
	}
}

func TestSupermarketNotFound(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	if _, err := s.Supermarkets().GetByCNPJ(ctx, "00000000000000"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
