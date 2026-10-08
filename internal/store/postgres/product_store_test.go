package store

import (
	"context"
	"errors"
	"testing"

	"meupreco/internal/domain"
)

func TestProductStoreLifecycle(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	p := &domain.Product{
		ProductKey: "07896008038717", // 13-digit GTIN stored, padded to 14 on write
		GTIN:       stringPtr("7896008038717"),
		Name:       "ARROZ BRANCO TIPO 1 5KG",
		Brand:      stringPtr("TIO JOAO"),
		Unit:       "UN",
	}
	if err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("create did not set ID")
	}
	if p.GTIN == nil || *p.GTIN != "07896008038717" {
		t.Errorf("gtin normalized = %v, want 14-digit 07896008038717", *p.GTIN)
	}

	byID, err := s.Products().GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.Name != p.Name {
		t.Errorf("name = %q", byID.Name)
	}

	byKey, err := s.Products().GetByKey(ctx, "07896008038717")
	if err != nil {
		t.Fatalf("get by key: %v", err)
	}
	if byKey.ID != p.ID {
		t.Errorf("get by key id = %d, want %d", byKey.ID, p.ID)
	}

	// GetByGTIN accepts the bare 13-digit barcode; the trigger normalizes.
	byGTIN, err := s.Products().GetByGTIN(ctx, "7896008038717")
	if err != nil {
		t.Fatalf("get by gtin: %v", err)
	}
	if byGTIN.ID != p.ID {
		t.Errorf("get by gtin id = %d, want %d", byGTIN.ID, p.ID)
	}
}

func TestProductStoreConflict(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	p := &domain.Product{ProductKey: "key-a", Name: "X", Unit: "UN"}
	if err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := s.Products().Create(ctx, &domain.Product{ProductKey: "key-a", Name: "Y", Unit: "UN"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate product_key = %v, want ErrConflict", err)
	}
}

// TestProductSearchTrgm proves the DoD case: pg_trgm accent/case-insensitive
// search, "limao" finds "Limão Tahiti" via the accent-stripped canonical stem.
func TestProductSearchTrgm(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	if err := s.Products().Create(ctx, &domain.Product{
		ProductKey:    "cm-limao-tahiti",
		Name:          "Limão Tahiti",
		CanonicalStem: stringPtr("limao tahiti"),
		Unit:          "UN",
	}); err != nil {
		t.Fatalf("create limao: %v", err)
	}
	if err := s.Products().Create(ctx, &domain.Product{
		ProductKey:    "cm-batata",
		Name:          "Batata Inglesa",
		CanonicalStem: stringPtr("batata inglesa"),
		Unit:          "KG",
	}); err != nil {
		t.Fatalf("create batata: %v", err)
	}

	hits, err := s.Products().Search(ctx, "limao", 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Name != "Limão Tahiti" {
		t.Fatalf("search 'limao' = %+v, want the Limão Tahiti product", hits)
	}

	// Case-insensitive: a capitalized query still matches.
	hits, err = s.Products().Search(ctx, "BATATA", 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Name != "Batata Inglesa" {
		t.Fatalf("search 'BATATA' = %+v", hits)
	}
}

func TestProductNotFound(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	if _, err := s.Products().GetByGTIN(ctx, "00000000000000"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.Products().GetByKey(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func stringPtr(s string) *string { return &s }
