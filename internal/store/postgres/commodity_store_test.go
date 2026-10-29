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
	// Regression for the deli-cheese stems seen in real receipts: the exact
	// stems the classifier now produces (brand words like VIGOR/TIROLEZ are
	// stripped before lookup) must resolve to their canonical commodity.
	s := testStore
	ctx := context.Background()

	cases := map[string]string{
		"qj mussarela":             "queijo mussarela", // map to self
		"qjo mussarela":            "queijo mussarela", // map to self
		"qj":                       "queijo mussarela", // abbreviation expansion
		"qjo":                      "queijo mussarela", // abbreviation expansion
		"qj parmesao":              "queijo parmesao",
		"qj parmesao vigor":        "queijo parmesao", // "QJ PARMESAO VIGOR KG" (brand off)
		"qj brie":                  "queijo brie",     // "QJ BRIE TIROLEZ KG" (brand stripped)
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
		t.Fatalf("duplicate synonym = %v, want ErrNotFound", err)
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

	queijo := "queijo"
	cases := []struct {
		stem        string
		expectedErr bool
	}{
		{"djdprato", true}, {"djd prato", true}, // invalid prefix
		{"qjprato", false}, {"qjo prato", false}, {"qjoprato", false}, {"qj prato", false},
		{"queijo prato", false},
	}

	goodRow := &domain.CommoditySynonym{
		Synonym: "qj", CanonicalName: "queijo mussarela", Unit: "KG", PrefixExpansion: &queijo,
	}
	if err := s.CommoditySynonyms().Create(ctx, goodRow); err != nil {
		t.Fatal(err)
	}
	badRow := &domain.CommoditySynonym{
		Synonym: "qjlegacy", CanonicalName: "queijo mussarela", Unit: "KG",
	}
	if err := s.CommoditySynonyms().Create(ctx, badRow); err != nil {
		t.Fatal(err)
	}

	got, err := s.CommoditySynonyms().MatchPrefix(ctx, "qjlegacy")
	if !errors.Is(err, domain.ErrNotFound) || got != nil {
		t.Errorf("matchprefix %q: expected ErrNotFound (NULL expansion row should be ignored), but got err=%v, row=+%v",
			"qjlegacy", err, got)
	}

	for _, res := range cases {
		got, err := s.CommoditySynonyms().MatchPrefix(ctx, res.stem)
		if !res.expectedErr && (err != nil || got == nil) {
			t.Errorf("matchprefix %q: unexpected error/nil = (%v,%+v)", res.stem, err, got)
			continue
		}

		switch res.stem {
		case "djdprato":
			if res.expectedErr && !(got == nil && errors.Is(err, domain.ErrNotFound)) {
				t.Errorf("matchprefix %q: expected ErrNotFound but got %+v/%v", res.stem, got, err)
				continue
			}
		case "djd prato":

			if res.expectedErr && !(got == nil && errors.Is(err, domain.ErrNotFound)) {
				t.Errorf("matchprefix %q: expected ErrNotFound but got %+v/%v", res.stem, got, err)
				continue
			}

		case "qjo prato": // longest match wins. Both qj and qjo have expansions so char_length DESC picks 3 > 2.
			if got == nil || !res.expectedErr && *got.PrefixExpansion != queijo {
				t.Errorf("%s != 'queijo', got=%v", *got.PrefixExpansion, got)
			}

		case "qjoprato": // longest match wins: both have expansions so char_length DESC picks 3 > 2.

			if got == nil || !res.expectedErr && *got.PrefixExpansion != queijo {
				t.Errorf("%s != 'queijo', got=%v", *got.PrefixExpansion, got)
			}

		case "qj prato": // qjo doesn't prefix this stem (left('qj prato', 3)=left of first 3 chars='qj ', syn='qjo' → no match). Falls back to qj.

			if got == nil || !res.expectedErr && *got.PrefixExpansion != queijo {
				t.Errorf("%s != 'queijo', got=%v", *got.PrefixExpansion, got)
			}

		case "queijo prato": // no abbreviation row has expansion set for this stem.

			if got == nil || !res.expectedErr && *got.PrefixExpansion != queijo {
				t.Errorf("%s != 'queijo', got=%v", *got.PrefixExpansion, got)
			}

		case "qjprato":

			if got == nil || !res.expectedErr && *got.PrefixExpansion != queijo {
				t.Errorf("%s != 'queijo', got=%v", *got.PrefixExpansion, got)
			}

		default:
			t.Fatalf("unexpected test case stem=%s", res.stem)
		}
	}
}
