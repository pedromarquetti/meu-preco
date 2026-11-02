package store

import (
	"context"
	"errors"
	"testing"

	"meupreco/internal/domain"
)

func TestCommoditySynonymLifecycle(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	expansion := "maca"
	c := &domain.CommoditySynonym{
		Synonym: "maca verde", CanonicalName: "maca verde", Category: "hortifruti", Unit: "KG", Source: "test",
		PrefixExpansion: &expansion,
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
	if got.PrefixExpansion == nil || *got.PrefixExpansion != expansion {
		t.Errorf("prefix expansion = %v, want %q", got.PrefixExpansion, expansion)
	}
}

func TestCommoditySynonymSeeded(t *testing.T) {
	// The 000002 migration seeds the produce taxonomy; after TestMain resets the
	// canonical row for "limao" exists even without test inserts.
	s := testStore
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
	// After the 000010/000011 cleanup the table contains only the abbreviation
	// prefixes, the full canonical names, and the cheese aliases. The classifier
	// (not this store test) expands "qj mofo" into "queijo gorgonzola"; here we
	// only assert the seeded rows themselves are present.
	s := testStore
	ctx := context.Background()

	cases := map[string]string{
		"qj":                "queijo mussarela", // abbreviation prefix row
		"qjo":               "queijo mussarela", // abbreviation prefix row
		"queijo mussarela":  "queijo mussarela",
		"queijo prato":      "queijo prato",
		"queijo parmesao":   "queijo parmesao",
		"queijo brie":       "queijo brie",
		"queijo gorgonzola": "queijo gorgonzola",
		"mofo":              "queijo gorgonzola", // cheese alias, only valid after a prefix
		"azul":              "queijo gorgonzola", // cheese alias, only valid after a prefix
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
	s := testStore
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

	queijo2 := "mussarela" // different value for prefix_expansion
	dup.PrefixExpansion = &queijo2
	if err := s.CommoditySynonyms().Create(ctx, &dup); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate synonym with new expansion should still conflict: %v", err)
	}

	if _, err := s.CommoditySynonyms().Canonicalize(ctx, "nao-existe"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing = %v, want ErrNotFound", err)
	}
}

func TestCommoditySynonymPrefixExpansion(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	// Migrations 000008/000009 seed qj/qjo (prefix_expansion = queijo) and the
	// identity prefix queijo. Keep those rows for the real-world cases and use
	// unique "zz" rows for custom expansion values so the test never collides
	// with the seeded taxonomy (truncate deliberately preserves it).
	if _, err := s.pool.Exec(ctx, `DELETE FROM commodity_synonyms WHERE synonym LIKE 'zz%'`); err != nil {
		t.Fatalf("clean test prefixes: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM commodity_synonyms WHERE synonym LIKE 'zz%'`)
	})

	queijo := "queijo"
	rows := []*domain.CommoditySynonym{
		{Synonym: "zzqj", CanonicalName: "queijo mussarela", Unit: "KG", PrefixExpansion: &queijo},
		{Synonym: "zzqjo", CanonicalName: "queijo mussarela", Unit: "KG", PrefixExpansion: &queijo},
		{Synonym: "zzlegacy", CanonicalName: "queijo mussarela", Unit: "KG"},
	}
	for _, r := range rows {
		if err := s.CommoditySynonyms().Create(ctx, r); err != nil {
			t.Fatalf("create %q: %v", r.Synonym, err)
		}
	}

	cases := []struct {
		stem        string
		wantSynonym string // empty means ErrNotFound
	}{
		{"djdprato", ""},
		{"djd prato", ""},
		{"qjprato", "qj"},
		{"qjmofo", "qj"},
		{"qjo prato", "qjo"},
		{"qjoprato", "qjo"}, // longest prefix wins
		{"qj prato", "qj"},
		{"zzqjprato", "zzqj"},
		{"zzqjo prato", "zzqjo"},
		{"zzqjoprato", "zzqjo"},    // longest prefix wins among test rows
		{"queijo prato", "queijo"}, // identity prefix seeded by 000009
		{"limao tahiti", ""},       // no prefix row
		{"zzlegacy", ""},           // nil expansion ignored
	}
	for _, tc := range cases {
		got, err := s.CommoditySynonyms().MatchPrefix(ctx, tc.stem)
		if tc.wantSynonym == "" {
			if got != nil || !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("MatchPrefix(%q) = (%+v, %v), want ErrNotFound", tc.stem, got, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("MatchPrefix(%q): %v", tc.stem, err)
			continue
		}
		if got.Synonym != tc.wantSynonym {
			t.Errorf("MatchPrefix(%q) synonym = %q, want %q", tc.stem, got.Synonym, tc.wantSynonym)
		}
	}
}
