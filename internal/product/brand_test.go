package product

import (
	"context"
	"errors"
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

	tirolezAbbrev := classify("QJ BRIE TIROLEZ KG")   // Expected success (Exact Match for qj brie)
	tirolezFull := classify("QUEIJO BRIE TIROLEZ KG") // Expected success (Exact Match for queijo brie)
	vigor := classify("QJO BRIE VIGOR KG")            // Should succeed if "Vigor" is stripped and "qjo brie" matches

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

func TestClassifyPrefixExpansion(t *testing.T) {
	// Case 1: Basic expansion (glued & spaced)
	catalog := &fakeCatalog{synonyms: map[string]*domain.CommoditySynonym{
		"qj":    {CanonicalName: "queijo", Synonym: "qj", PrefixExpansion: stringPtr("queijo")},
		"qjo":   {CanonicalName: "queijo", Synonym: "qjo", PrefixExpansion: stringPtr("queijo")},
		"prato": {CanonicalName: "prato", Synonym: "prato"},
	}}

	cases := []struct {
		desc string
		want string
	}{
		{"qjoprato", "queijo prato"}, // qjo -> queijo (prefix) + prato (exact match/remainder)
		{"qjprato", "queijo prato"},  // qj -> queijo (prefix) + prato (exact match/remainder)
		{"QJ PRATO", "queijo prato"}, // QJ -> queijo (prefix) + PRATO (exact match/remainder)
	}

	t.Log("Product Test")
	for _, i := range cases {
		line := Line{Description: i.desc, Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(10)}
		decision := Classify(context.Background(), catalog, line, nil)
		t.Logf("desc=%v want=%v got=%v", i.desc, i.want, decision.Name)
		if decision.Name != i.want {
			t.Errorf("Classify(%q) = %q, want %q", i.desc, decision.Name, i.want)
		}
	}

	// Case 2: More complex scenarios
	cat2 := &fakeCatalog{synonyms: map[string]*domain.CommoditySynonym{
		"qj":           {CanonicalName: "queijo", Synonym: "qj", PrefixExpansion: stringPtr("queijo")},
		"qjo":          {CanonicalName: "queijo", Synonym: "qjo", PrefixExpansion: stringPtr("queijo")},
		"brie":         {CanonicalName: "brie", Synonym: "brie"},
		"prato":        {CanonicalName: "prato", Synonym: "prato"},
		"queijo prato": {CanonicalName: "queijo prato", Synonym: "queijo prato"},
		"queijo brie":  {CanonicalName: "queijo brie", Synonym: "queijo brie"},
	}}
	brands2 := NewBrandDictionary([]string{"Tirolez", "Vigor"})

	cases2 := []struct {
		desc      string
		want      string
		wantBrand string
	}{
		{"QJO PRATO KG", "queijo prato", ""}, // qjo -> queijo (prefix) + prato
		{"QUEIJO PRATO", "queijo prato", ""}, // exact hit (unchanged)
		{"QJ BRIE TIROLEZ KG", "queijo brie", "Tirolez"},
		{"QJBRIE TIROLEZ KG", "queijo brie", "Tirolez"},
		{"QJOBRIE TIROLEZ KG", "queijo brie", "Tirolez"},
		{"QJO BRIE TIROLEZ KG", "queijo brie", "Tirolez"},
		{"QJ BRIE VIGOR KG", "queijo brie", "Vigor"},
	}

	t.Log("Brand test")
	for _, i := range cases2 {
		line := Line{Description: i.desc, Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(10)}
		decision := Classify(context.Background(), cat2, line, brands2)
		t.Logf("desc=%v wantProd=%v got=%v, wantBrand=%v gotBrand=%v", i.desc, i.want, decision.Name, i.wantBrand, decision.Brand)

		if decision.Status != StatusCommodityFingerprint {
			t.Errorf("Classify(%q) status = %q, want commodity_fingerprint", i.desc, decision.Status)
		}
		if decision.Name != i.want {
			t.Errorf("Classify(%q) = %q, want %q", i.desc, decision.Name, i.want)
		}
		if decision.Brand != i.wantBrand {
			t.Errorf("Classify(%q) brand = %q, want %q", i.desc, decision.Brand, i.wantBrand)
		}

		wantKey := CommodityKey(i.want, "KG", i.wantBrand)
		if decision.ProductKey != wantKey {
			t.Errorf("Classify(%q) key = %q, want %q", i.desc, decision.ProductKey, wantKey)
		}
	}

	// Case 3: Longest prefix wins. The two prefixes expand to distinct values so
	// the assertion proves qjo was selected rather than qj.
	cat3 := &fakeCatalog{synonyms: map[string]*domain.CommoditySynonym{
		"qj":    {CanonicalName: "queijo_1", Synonym: "qj", PrefixExpansion: stringPtr("queijo a")},
		"qjo":   {CanonicalName: "queijo_2", Synonym: "qjo", PrefixExpansion: stringPtr("queijo b")},
		"prato": {CanonicalName: "prato", Synonym: "prato"},
	}}

	// qjoprato should match 'qjo' because it is longer than 'qj'.
	d := Classify(context.Background(), cat3, Line{Description: "qjoprato", Qty: decimal.NewFromInt(1), Unit: "KG"}, nil)
	if d.Name != "queijo b prato" {
		t.Errorf("Longest prefix wins failed: expected 'queijo b prato', got %q", d.Name)
	}
	if wantKey := CommodityKey("queijo b prato", "KG", ""); d.ProductKey != wantKey {
		t.Errorf("Longest prefix key = %q, want %q", d.ProductKey, wantKey)
	}

	// Case 4: Exact match precedence (Phase 3 requirement check)
	cat4 := &fakeCatalog{synonyms: map[string]*domain.CommoditySynonym{
		"qj":       {CanonicalName: "queijo_1", Synonym: "qj", PrefixExpansion: stringPtr("queijo")},
		"prato":    {CanonicalName: "prato", Synonym: "prato"},
		"qjoprato": {CanonicalName: "exact_match", Synonym: "qjoprato"}, // Exact match row exists
	}}

	d = Classify(context.Background(), cat4, Line{Description: "qjoprato", Qty: decimal.NewFromInt(1), Unit: "KG"}, nil)
	if d.Name != "exact_match" {
		t.Errorf("Exact match precedence failed: expected 'exact_match', got %q", d.Name)
	}

	// Case 5: MatchPrefix error propagates as uid_uncertain.
	cat5 := &fakeCatalog{prefixErr: errors.New("prefix lookup failed")}
	d = Classify(context.Background(), cat5, Line{Description: "qjoprato", Qty: decimal.NewFromInt(1), Unit: "KG"}, nil)
	if d.Status != StatusUIDUncertain {
		t.Errorf("MatchPrefix error: status = %q, want uid_uncertain", d.Status)
	}
}

func TestClassifyPrefixExpansionUsesCanonicalAlias(t *testing.T) {
	// "qj mofo" expands to "queijo mofo"; since that exact row was dropped,
	// the classifier resolves the remainder ("mofo") as a cheese alias for
	// "queijo gorgonzola". Standalone "mofo" must not resolve.
	cat := &fakeCatalog{synonyms: map[string]*domain.CommoditySynonym{
		"qj":                {CanonicalName: "queijo mussarela", Synonym: "qj", Unit: "KG", PrefixExpansion: stringPtr("queijo")},
		"mofo":              {CanonicalName: "queijo gorgonzola", Synonym: "mofo", Unit: "KG", Source: "cheese_alias"},
		"azul":              {CanonicalName: "queijo gorgonzola", Synonym: "azul", Unit: "KG", Source: "cheese_alias"},
		"queijo gorgonzola": {CanonicalName: "queijo gorgonzola", Synonym: "queijo gorgonzola", Unit: "KG"},
	}}

	line := Line{Description: "QJ MOFO KG", Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(10)}
	d := Classify(context.Background(), cat, line, nil)
	if d.Status != StatusCommodityFingerprint {
		t.Fatalf("status = %q, want commodity_fingerprint", d.Status)
	}
	if d.Name != "queijo gorgonzola" || d.CanonicalStem != "queijo gorgonzola" {
		t.Errorf("decision = %+v, want canonical queijo gorgonzola", d)
	}
	if wantKey := CommodityKey("queijo gorgonzola", "KG", ""); d.ProductKey != wantKey {
		t.Errorf("product key = %q, want %q", d.ProductKey, wantKey)
	}

	// Standalone "mofo" must not resolve to gorgonzola.
	standalone := Classify(context.Background(), cat, Line{Description: "MOFO", Qty: decimal.NewFromInt(1), Unit: "KG"}, nil)
	if standalone.Status == StatusCommodityFingerprint {
		t.Errorf("standalone mofo should not map to a commodity: %+v", standalone)
	}
}

func stringPtr(s string) *string { return &s }

// TestClassifyParserExampleCheeseVariants mirrors the Parser example table:
// every abbreviation/brand variant must collapse onto the same canonical
// commodity without per-product combination rows.
func TestClassifyParserExampleCheeseVariants(t *testing.T) {
	queijo := "queijo"
	cat := &fakeCatalog{synonyms: map[string]*domain.CommoditySynonym{
		"qj":     {CanonicalName: "queijo mussarela", Synonym: "qj", Unit: "KG", PrefixExpansion: &queijo},
		"qjo":    {CanonicalName: "queijo mussarela", Synonym: "qjo", Unit: "KG", PrefixExpansion: &queijo},
		"queijo": {CanonicalName: "queijo mussarela", Synonym: "queijo", Unit: "KG", PrefixExpansion: &queijo},

		"queijo prato":      {CanonicalName: "queijo prato", Synonym: "queijo prato", Unit: "KG"},
		"queijo brie":       {CanonicalName: "queijo brie", Synonym: "queijo brie", Unit: "KG"},
		"queijo gorgonzola": {CanonicalName: "queijo gorgonzola", Synonym: "queijo gorgonzola", Unit: "KG"},

		"mofo": {CanonicalName: "queijo gorgonzola", Synonym: "mofo", Unit: "KG", Source: "cheese_alias"},
		"azul": {CanonicalName: "queijo gorgonzola", Synonym: "azul", Unit: "KG", Source: "cheese_alias"},
	}}
	brands := NewBrandDictionary([]string{"Tirolez"})

	cases := []struct {
		desc      string
		wantName  string
		wantBrand string
	}{
		{"QJO PRATO KG", "queijo prato", ""},
		{"QJOPRATO", "queijo prato", ""},
		{"QJ MOFO", "queijo gorgonzola", ""},
		{"QJO MOFO", "queijo gorgonzola", ""},
		{"QJOMOFO", "queijo gorgonzola", ""},
		{"QJ AZUL", "queijo gorgonzola", ""},
		{"QJO AZUL", "queijo gorgonzola", ""},
		{"QJOAZUL", "queijo gorgonzola", ""},
		{"QJAZUL", "queijo gorgonzola", ""},
		{"QJPRATO", "queijo prato", ""},
		{"QJ PRATO", "queijo prato", ""},
		{"QJO BRIE TIROLEZ KG", "queijo brie", "Tirolez"},
		{"QJ BRIE TIROLEZ KG", "queijo brie", "Tirolez"},
		{"QUEIJO PRATO", "queijo prato", ""},
		// Full-word cheese prefix with alias words (the live COVABRA line).
		{"QUEIJO MOFO", "queijo gorgonzola", ""},
		{"QUEIJO AZUL", "queijo gorgonzola", ""},
		{"QUEIJO MOFO AZUL", "queijo gorgonzola", ""},
		{"QUEIJO MOFO AZUL TIROLEZ KG", "queijo gorgonzola", "Tirolez"},
		{"QJ MOFO AZUL TIROLEZ KG", "queijo gorgonzola", "Tirolez"},
		{"QJO MOFO AZUL", "queijo gorgonzola", ""},
	}
	for _, tc := range cases {
		line := Line{Description: tc.desc, Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(10)}
		d := Classify(context.Background(), cat, line, brands)
		if d.Status != StatusCommodityFingerprint {
			t.Errorf("%q: status = %q, want commodity_fingerprint", tc.desc, d.Status)
			continue
		}
		if d.Name != tc.wantName || d.CanonicalStem != tc.wantName {
			t.Errorf("%q: name/stem = %q/%q, want %q", tc.desc, d.Name, d.CanonicalStem, tc.wantName)
		}
		if d.Brand != tc.wantBrand {
			t.Errorf("%q: brand = %q, want %q", tc.desc, d.Brand, tc.wantBrand)
		}
	}

	// Ambiguous aliases must stay scoped to cheese: standalone "mofo"/"azul"
	// and non-alias remainder words never collapse onto gorgonzola.
	for _, desc := range []string{"MOFO", "AZUL", "QUEIJO MOFO PRATO"} {
		d := Classify(context.Background(), cat, Line{Description: desc, Qty: decimal.NewFromInt(1), Unit: "KG", TotalPrice: decimal.NewFromInt(10)}, brands)
		if d.Status == StatusCommodityFingerprint {
			t.Errorf("%q: unexpectedly matched %q", desc, d.Name)
		}
	}
}
