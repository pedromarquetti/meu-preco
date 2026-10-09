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

func TestCommoditySynonymSeededCheese(t *testing.T) {
	// Regression for the deli-cheese stems seen in real receipts — the exact
	// NormalizeStem output (lowercase, accent-stripped, brand tokens like
	// VIGOR/TIROLEZ retained) that must flip to commodity_fingerprint.
	s := mustStore(t)
	ctx := context.Background()

	cases := map[string]string{
		"qj mussarela":             "queijo mussarela", // map to self
		"qjo mussarela":            "queijo mussarela", // map to self
		"qj":                       "queijo mussarela", // abbreviation expansion
		"qjo":                      "queijo mussarela", // abbreviation expansion
		"qj parmesao":              "queijo parmesao",
		"qj parmesao vigor":        "queijo parmesao", // "QJ PARMESAO VIGOR KG"
		"qjo brie":                 "queijo brie",
		"qjo brie vigor":           "queijo brie", // "QJO BRIE VIGOR KG"
		"queijo mofo":              "queijo gorgonzola",
		"queijo azul":              "queijo gorgonzola",
		"queijo mofo azul":         "queijo gorgonzola",
		"queijo mofo azul tirolez": "queijo gorgonzola", // "QUEIJO MOFO AZUL TIROLEZ"
	}
	for stem, want := range cases {
		got, err := s.CommoditySynonyms().Canonicalize(ctx, stem)
		if err != nil {
			t.Errorf("canonicalize %q: %v", stem, err)
			continue
		}
		if got.CanonicalName != want {
			t.Errorf("canonicalize %q = %q, want %q", stem, got.CanonicalName, want)
		}
		if got.Unit != "KG" {
			t.Errorf("canonicalize %q unit = %q, want KG", stem, got.Unit)
		}
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
