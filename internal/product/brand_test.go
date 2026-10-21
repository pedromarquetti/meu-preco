package product

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

func TestNewBrandDictionaryPhraseMatching(t *testing.T) {
	d := NewBrandDictionaryEntries([]BrandEntry{
		{Key: "tirolez", Display: "Tirolez"},
		{Key: "tio joao", Display: "Tio João"},
		{Key: "3 coracoes", Display: "3 Corações"},
		{Key: "hellmanns", Display: "Hellmann's"},
	})

	cases := []struct {
		desc  string
		want  string
		found bool
	}{
		{"QJ BRIE TIROLEZ KG", "Tirolez", true},
		{"ARROZ TIO JOAO T1", "Tio João", true},
		{"CAFE 3 CORACOES 500G", "3 Corações", true},
		{"MAIONESE HELLMANNS 500G", "Hellmann's", true},
		{"QUEIJO BRIE KG", "", false},
	}
	for _, tc := range cases {
		got, ok := ExtractBrand(tc.desc, d)
		if got != tc.want || ok != tc.found {
			t.Errorf("ExtractBrand(%q) = (%q,%v), want (%q,%v)", tc.desc, got, ok, tc.want, tc.found)
		}
	}
}

func TestNormalizeStemStripsBrandPhrase(t *testing.T) {
	d := NewBrandDictionary([]string{"Tirolez", "Tio João"})
	if got := NormalizeStem("QJ BRIE TIROLEZ KG", "KG", d); got != "qj brie" {
		t.Errorf("NormalizeStem = %q, want %q", got, "qj brie")
	}
	if got := NormalizeStem("ARROZ TIO JOAO", "", d); got != "arroz" {
		t.Errorf("NormalizeStem = %q, want %q", got, "arroz")
	}
}

// TestClassifyBrandSeparatesCommodity proves the live-receipt regression:
// "QJ BRIE TIROLEZ KG" and "QUEIJO BRIE TIROLEZ KG" collapse onto the same
// brand-scoped identity, while Vigor's brie is a distinct product.
func TestClassifyBrandSeparatesCommodity(t *testing.T) {
	cat := &fakeCatalog{synonyms: map[string]*domain.CommoditySynonym{
		"qj brie":     {CanonicalName: "queijo brie", Category: "laticinios", Unit: "KG"},
		"qjo brie":    {CanonicalName: "queijo brie", Category: "laticinios", Unit: "KG"},
		"queijo brie": {CanonicalName: "queijo brie", Category: "laticinios", Unit: "KG"},
	}}
	brands := NewBrandDictionary([]string{"Tirolez", "Vigor"})

	classify := func(desc string) Decision {
		t.Helper()
		line := Line{Description: desc, Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(10)}
		return Classify(context.Background(), cat, line, brands)
	}

	tirolezAbbrev := classify("QJ BRIE TIROLEZ KG")
	tirolezFull := classify("QUEIJO BRIE TIROLEZ KG")
	vigor := classify("QJO BRIE VIGOR KG")

	for name, d := range map[string]Decision{"abbrev": tirolezAbbrev, "full": tirolezFull, "vigor": vigor} {
		if d.Status != StatusCommodityFingerprint {
			t.Fatalf("%s: status = %q, want commodity_fingerprint", name, d.Status)
		}
		if d.Name != "queijo brie" || d.CanonicalStem != "queijo brie" {
			t.Errorf("%s: name/stem = %q/%q, want queijo brie", name, d.Name, d.CanonicalStem)
		}
	}

	wantTirolez := CommodityKey("queijo brie", "KG", "Tirolez")
	if tirolezAbbrev.ProductKey != wantTirolez || tirolezFull.ProductKey != wantTirolez {
		t.Errorf("Tirolez keys = %q/%q, want %q", tirolezAbbrev.ProductKey, tirolezFull.ProductKey, wantTirolez)
	}
	if tirolezAbbrev.Brand != "Tirolez" || tirolezFull.Brand != "Tirolez" {
		t.Errorf("Tirolez brands = %q/%q", tirolezAbbrev.Brand, tirolezFull.Brand)
	}

	wantVigor := CommodityKey("queijo brie", "KG", "Vigor")
	if vigor.ProductKey != wantVigor || vigor.Brand != "Vigor" {
		t.Errorf("Vigor key/brand = %q/%q, want %q/Vigor", vigor.ProductKey, vigor.Brand, wantVigor)
	}
	if vigor.ProductKey == tirolezAbbrev.ProductKey {
		t.Error("different brands must not collide on one product key")
	}
}

// TestClassifyRejectsKeylessNearestMatch guards the phantom-product bug: a
// legacy catalog row with no product_key must route to review, not mint a
// blank-key product on commit.
func TestClassifyRejectsKeylessNearestMatch(t *testing.T) {
	cat := &fakeCatalog{nearest: &nearestResult{
		p:   &domain.Product{ProductKey: "", Name: "Queijo Brie", Unit: "KG"},
		sim: 0.7,
	}}
	line := Line{Description: "QUEIJO BRIE TIROLEZ KG", Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(10)}
	d := Classify(context.Background(), cat, line, nil)
	if d.Status != StatusUIDUncertain {
		t.Fatalf("status = %q, want uid_uncertain", d.Status)
	}
}

func TestClassifySetsCanonicalStemFromCatalog(t *testing.T) {
	cat := &fakeCatalog{synonyms: map[string]*domain.CommoditySynonym{
		"queijo brie": {CanonicalName: "queijo brie", Unit: "KG"},
	}}
	line := Line{Description: "QUEIJO BRIE KG", Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(10)}
	d := Classify(context.Background(), cat, line, nil)
	if d.CanonicalStem != "queijo brie" {
		t.Errorf("canonical stem = %q, want queijo brie", d.CanonicalStem)
	}
}
