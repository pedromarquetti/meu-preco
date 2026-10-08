package store

import (
	"context"
	"errors"
	"testing"

	"meupreco/internal/domain"
)

func TestCommoditySynonymLifecycle(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	c := &domain.CommoditySynonym{
		Synonym: "maca verde", CanonicalName: "maca verde", Category: "hortifruti", Unit: "KG", Source: "test",
	}
	if err := s.CommoditySynonyms().Create(ctx, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.ID == 0 {
		t.Fatal("create did not set ID")
	}

	got, err := s.CommoditySynonyms().Canonicalize(ctx, "maca verde")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if got.CanonicalName != "maca verde" || got.Unit != "KG" {
		t.Errorf("got = %+v", got)
	}
}

func TestCommoditySynonymSeeded(t *testing.T) {
	// The 000002 migration seeds the produce taxonomy; after resetSchema the
	// canonical row for "limao" exists even without test inserts.
	s := mustStore(t)
	ctx := context.Background()

	got, err := s.CommoditySynonyms().Canonicalize(ctx, "limao tahiti")
	if err != nil {
		t.Fatalf("canonicalize seeded: %v", err)
	}
	if got.CanonicalName != "limao tahiti" {
		t.Errorf("canonical = %q", got.CanonicalName)
	}
}

func TestCommoditySynonymConflictAndNotFound(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	syn := &domain.CommoditySynonym{Synonym: "fruta-do-conde-test", CanonicalName: "fruta do conde"}
	if err := s.CommoditySynonyms().Create(ctx, syn); err != nil {
		t.Fatalf("create: %v", err)
	}
	dup := *syn
	dup.ID = 0
	if err := s.CommoditySynonyms().Create(ctx, &dup); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate synonym = %v, want ErrConflict", err)
	}

	if _, err := s.CommoditySynonyms().Canonicalize(ctx, "nao-existe"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing = %v, want ErrNotFound", err)
	}
}
