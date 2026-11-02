package service

import (
	"context"
	"testing"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

// seedCheeseTaxonomy installs the abbreviation prefixes, full canonical names
// and ambiguous cheese aliases used by the parser-example table. Only one row
// per abbreviation is needed: the prefix is expanded before lookup, so no
// "qj prato" / "qjo brie" combinations exist.
func seedCheeseTaxonomy(t *testing.T, m *memStores) {
	t.Helper()
	ctx := context.Background()

	for _, syn := range []string{"qj", "qjo", "queijo"} {
		if err := m.CommoditySynonyms().Create(ctx, &domain.CommoditySynonym{
			Synonym:         syn,
			CanonicalName:   "queijo mussarela",
			Category:        "laticinios",
			Unit:            product.UnitKG,
			PrefixExpansion: strPtr("queijo"),
		}); err != nil {
			t.Fatalf("seed prefix %q: %v", syn, err)
		}
	}
	for _, syn := range []string{"queijo mussarela", "queijo prato", "queijo brie", "queijo gorgonzola"} {
		if err := m.CommoditySynonyms().Create(ctx, &domain.CommoditySynonym{
			Synonym:       syn,
			CanonicalName: syn,
			Category:      "laticinios",
			Unit:          product.UnitKG,
		}); err != nil {
			t.Fatalf("seed exact %q: %v", syn, err)
		}
	}
	for _, syn := range []string{"mofo", "azul"} {
		if err := m.CommoditySynonyms().Create(ctx, &domain.CommoditySynonym{
			Synonym:       syn,
			CanonicalName: "queijo gorgonzola",
			Category:      "laticinios",
			Unit:          product.UnitKG,
			Source:        product.CheeseAliasSource,
		}); err != nil {
			t.Fatalf("seed cheese alias %q: %v", syn, err)
		}
	}
}

// TestBrandPlanSuggestedStems proves the admin review queue mirrors the
// classifier: glued/spaced abbreviations expand, and ambiguous cheese words
// only resolve after a cheese prefix.
func TestBrandPlanSuggestedStems(t *testing.T) {
	ctx := context.Background()
	m := newMemStores()
	seedCheeseTaxonomy(t, m)
	svc := New(m, stubResolver{}, nil, nil)
	brands := product.NewBrandDictionary([]string{"Tirolez"})

	tests := []struct {
		description string
		unit        string
		wantStem    string
	}{
		{"QJO PRATO KG", "KG", "queijo prato"},
		{"QJOPRATO", "", "queijo prato"},
		{"QJ PRATO", "KG", "queijo prato"},
		{"QUEIJO PRATO", "KG", "queijo prato"},
		{"QJ MOFO", "KG", "queijo gorgonzola"},
		{"QJO AZUL", "KG", "queijo gorgonzola"},
		{"QJAZUL", "KG", "queijo gorgonzola"},
		{"QJOAZUL", "KG", "queijo gorgonzola"},
		{"QUEIJO MOFO AZUL TIROLEZ", "KG", "queijo gorgonzola"},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			if got := svc.suggestStem(ctx, tc.description, tc.unit, brands); got != tc.wantStem {
				t.Errorf("suggestStem(%q) = %q, want %q", tc.description, got, tc.wantStem)
			}
		})
	}

	// Ambiguous words must not resolve without a cheese prefix.
	for _, desc := range []string{"MOFO", "AZUL"} {
		if got := svc.suggestStem(ctx, desc, "KG", brands); got != "mofo" && got != "azul" {
			t.Errorf("suggestStem(%q) = %q, want the raw stem", desc, got)
		}
	}
}
