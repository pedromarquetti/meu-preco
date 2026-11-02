package service

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

func mustDecimal(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// TestCheeseParsingRegression replays the Parser example table end to end
// against the in-memory stores: abbreviation prefixes expand, ambiguous cheese
// words collapse onto gorgonzola, and brands scope the identity. No
// "<prefix> <product>" combination rows are seeded.
func TestCheeseParsingRegression(t *testing.T) {
	ctx := context.Background()
	m := newMemStores()
	seedCheeseTaxonomy(t, m)
	brands := product.NewBrandDictionary([]string{"Tirolez"})

	tests := []struct {
		description string
		unit        string
		wantName    string
		wantBrand   string
	}{
		{"QJO PRATO KG", "KG", "queijo prato", ""},
		{"QJOPRATO", "KG", "queijo prato", ""},
		{"QJ PRATO", "KG", "queijo prato", ""},
		{"QJPRATO", "KG", "queijo prato", ""},
		{"QUEIJO PRATO", "KG", "queijo prato", ""},
		{"QJ MOFO", "KG", "queijo gorgonzola", ""},
		{"QJO MOFO", "KG", "queijo gorgonzola", ""},
		{"QJOMOFO", "KG", "queijo gorgonzola", ""},
		{"QJ AZUL", "KG", "queijo gorgonzola", ""},
		{"QJO AZUL", "KG", "queijo gorgonzola", ""},
		{"QJOAZUL", "KG", "queijo gorgonzola", ""},
		{"QJAZUL", "KG", "queijo gorgonzola", ""},
		{"QUEIJO MOFO AZUL TIROLEZ", "KG", "queijo gorgonzola", "Tirolez"},
		{"QJO BRIE TIROLEZ KG", "KG", "queijo brie", "Tirolez"},
		{"QJ BRIE TIROLEZ KG", "KG", "queijo brie", "Tirolez"},
	}

	for _, tc := range tests {
		t.Run(tc.description, func(t *testing.T) {
			line := product.Line{
				Description: tc.description,
				Qty:         mustDecimal("1"),
				Unit:        tc.unit,
				TotalPrice:  mustDecimal("10"),
			}
			d := product.Classify(ctx, m.Catalog(), line, brands)

			if d.Status != product.StatusCommodityFingerprint {
				t.Fatalf("status = %q, want commodity_fingerprint (input: %s, msg: %s)", d.Status, tc.description, d.Message)
			}
			if d.Name != tc.wantName || d.CanonicalStem != tc.wantName {
				t.Errorf("name/stem = %q/%q, want %q", d.Name, d.CanonicalStem, tc.wantName)
			}
			if d.Brand != tc.wantBrand {
				t.Errorf("brand = %q, want %q", d.Brand, tc.wantBrand)
			}
			wantKey := product.CommodityKey(tc.wantName, product.UnitKG, tc.wantBrand)
			if d.ProductKey != wantKey {
				t.Errorf("product key = %q, want %q", d.ProductKey, wantKey)
			}
		})
	}

	// Ambiguous words stay scoped to cheese: standalone "mofo"/"azul" and a
	// mixed alias+product phrase must not mint a commodity.
	for _, desc := range []string{"MOFO", "AZUL", "QUEIJO MOFO PRATO"} {
		line := product.Line{Description: desc, Qty: mustDecimal("1"), Unit: "KG", TotalPrice: mustDecimal("10")}
		d := product.Classify(ctx, m.Catalog(), line, brands)
		if d.Status == product.StatusCommodityFingerprint {
			t.Errorf("%q: unexpectedly matched %q", desc, d.Name)
		}
	}

	t.Run("Exact precedence", func(t *testing.T) {
		if err := m.CommoditySynonyms().Create(ctx, &domain.CommoditySynonym{
			Synonym:       "qjoprato",
			CanonicalName: "queijo prato especial",
			Unit:          product.UnitKG,
		}); err != nil {
			t.Fatalf("seed exact row: %v", err)
		}

		line := product.Line{Description: "qjoprato", Qty: mustDecimal("1"), Unit: "KG", TotalPrice: mustDecimal("10")}
		d := product.Classify(ctx, m.Catalog(), line, nil)

		if d.Status != product.StatusCommodityFingerprint {
			t.Fatalf("status = %q, want commodity_fingerprint", d.Status)
		}
		if d.Name != "queijo prato especial" {
			t.Errorf("name = %q, want the exact row to win", d.Name)
		}
	})

	t.Run("Longest prefix wins", func(t *testing.T) {
		m2 := newMemStores()
		for syn, expansion := range map[string]string{"qj": "queijo a", "qjo": "queijo b"} {
			if err := m2.CommoditySynonyms().Create(ctx, &domain.CommoditySynonym{
				Synonym: syn, PrefixExpansion: strPtr(expansion),
			}); err != nil {
				t.Fatalf("seed prefix %q: %v", syn, err)
			}
		}

		line := product.Line{Description: "qjoprato", Qty: mustDecimal("1"), Unit: "KG", TotalPrice: mustDecimal("10")}
		d := product.Classify(ctx, m2.Catalog(), line, nil)

		if d.ProductKey != product.CommodityKey("queijo b prato", "KG", "") {
			t.Errorf("expected longest prefix (qjo) to win, got name %q key %q", d.Name, d.ProductKey)
		}
	})
}
