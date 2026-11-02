package service

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

// TestCreateSynonymPipeline proves an admin-created synonym row is immediately
// usable by the classifier: the normalized key feeds exact lookup and an
// optional prefix_expansion feeds abbreviation expansion.
func TestCreateSynonymPipeline(t *testing.T) {
	ctx := context.Background()
	m := newMemStores()
	svc := New(m, stubResolver{}, nil, nil)

	// Map the abbreviation qj -> queijo (prefix expansion).
	prefix, err := svc.CreateSynonym(ctx, SynonymRequest{
		Synonym:         "FARTRIGO",
		CanonicalName:   "farinha de trigo",
		Category:        "farinha",
		Unit:            "kg",
		PrefixExpansion: strPtr("farinha"),
	})
	if err != nil {
		t.Fatalf("create prefix: %v", err)
	}

	if prefix.Synonym != "fartrigo" || prefix.CanonicalName != "farinha de trigo" {
		t.Errorf("wantedSynonym=FARTRIGO> wanted %v; wantedCanonical=farinha de trigo, got %v; prefix normalized = %+v", prefix.Synonym, prefix.CanonicalName, prefix)
	}
	if prefix.Unit != product.UnitKG || prefix.PrefixExpansion == nil || *prefix.PrefixExpansion != "farinha" {
		t.Errorf("prefix unit/expansion = %q/%v", prefix.Unit, prefix.PrefixExpansion)
	}
	if prefix.Source != "admin" {
		t.Errorf("default source = %q, want admin", prefix.Source)
	}
	if prefix.ID == 0 {
		t.Error("created synonym has no id")
	}

	// A plain exact synonym and an ambiguous cheese alias.
	if _, err := svc.CreateSynonym(ctx, SynonymRequest{
		Synonym: "mussarela", CanonicalName: "queijo mussarela", Unit: "KG",
	}); err != nil {
		t.Fatalf("create exact synonym: %v", err)
	}
	if _, err := svc.CreateSynonym(ctx, SynonymRequest{
		Synonym: "mofo", CanonicalName: "queijo gorgonzola", Unit: "KG",
		Source: product.CheeseAliasSource,
	}); err != nil {
		t.Fatalf("create cheese alias: %v", err)
	}

	// The rows are readable through the store interface.
	if got, err := m.CommoditySynonyms().Canonicalize(ctx, "mussarela"); err != nil {
		t.Fatalf("canonicalize mussarela: %v", err)
	} else if got.CanonicalName != "queijo mussarela" {
		t.Errorf("mussarela -> %q, want queijo mussarela", got.CanonicalName)
	}
	if got, err := m.CommoditySynonyms().MatchPrefix(ctx, "fartrigo"); err != nil {
		t.Fatalf("match prefix: %v", err)
	} else if got.Synonym != "fartrigo" {
		t.Errorf("match prefix = %q, want fartrigo", got.Synonym)
	}

	// End to end through Classify: exact and prefix+alias paths both resolve.
	classify := func(desc string) product.Decision {
		t.Helper()
		line := product.Line{Description: desc, Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(10)}
		return product.Classify(ctx, m.Catalog(), line, nil)
	}

	exact := classify("MUSSARELA")
	if exact.Status != product.StatusCommodityFingerprint || exact.Name != "queijo mussarela" {
		t.Errorf("MUSSARELA = %+v, want queijo mussarela", exact)
	}

	_, er := svc.CreateSynonym(ctx, SynonymRequest{
		Synonym:         " QJ ",
		CanonicalName:   "Queijo Mussarela",
		Category:        "laticinios",
		Unit:            "kg",
		PrefixExpansion: strPtr("queijo"),
	})
	if er != nil {
		t.Fatalf("create prefix: %v", err)
	}

	prefixed := classify("QJ MOFO")
	if prefixed.Status != product.StatusCommodityFingerprint || prefixed.Name != "queijo gorgonzola" {
		t.Errorf("QJ MOFO = %+v, want queijo gorgonzola", prefixed)
	}

	// Duplicates are conflicts, not silent overwrites.
	if _, err := svc.CreateSynonym(ctx, SynonymRequest{
		Synonym: "qj", CanonicalName: "queijo prato",
	}); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate err = %v, want domain.ErrConflict", err)
	}
}

func TestCreateSynonymValidation(t *testing.T) {
	ctx := context.Background()
	svc := New(newMemStores(), stubResolver{}, nil, nil)

	bad := []struct {
		name string
		req  SynonymRequest
	}{
		{"empty synonym", SynonymRequest{CanonicalName: "queijo prato"}},
		{"empty canonical", SynonymRequest{Synonym: "qj"}},
		{"invalid unit", SynonymRequest{Synonym: "qj", CanonicalName: "queijo", Unit: "liters"}},
		{"empty prefix expansion", SynonymRequest{Synonym: "qj", CanonicalName: "queijo", PrefixExpansion: strPtr("  ")}},
		{"numeric-only synonym", SynonymRequest{Synonym: "500", CanonicalName: "queijo"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.CreateSynonym(ctx, tc.req); !errors.Is(err, ErrInvalidAssignment) {
				t.Errorf("err = %v, want ErrInvalidAssignment", err)
			}
		})
	}
}
