package product

import "testing"

func TestNormalizeStem(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		unit   string
		brands []string
		want   string
	}{
		{
			name: "produce accent strip + unit drop",
			in:   "LIMAO TAHITI KG",
			unit: "KG",
			want: "limao tahiti",
		},
		{
			name: "test strip multiple spaces",
			in:   "LIMAO    TAHITI KG",
			unit: "KG",
			want: "limao tahiti",
		},
		{
			name: "pack tokens removed (EMB)",
			in:   "ARROZ BRANCO EMB 5KG",
			unit: "",
			want: "arroz branco",
		},
		{
			name: "pack tokens removed (CAIXA)",
			in:   "MAMAO 12 CAIXA",
			unit: "",
			want: "mamao",
		},
		{
			name: "pack tokens removed (SACO/PET)",
			in:   "BATATA INGLESA SACO PET 1KG",
			unit: "KG",
			want: "batata inglesa",
		},
		{
			name: "numeric only dropped",
			in:   "BANANA 1234 0",
			unit: "",
			want: "banana",
		},
		{
			name: "unit token literal dropped",
			in:   "QUEIJO MINAS",
			unit: "KG",
			want: "queijo minas",
		},
		{
			name:   "brand token removed from dictionary",
			in:     "ARROZ TIO JOAO 5KG",
			unit:   "KG",
			brands: []string{"Tio Joao"},
			want:   "arroz",
		},
		{
			name: "single letters dropped",
			in:   "PDE MEIO PEITO FRANGO P",
			unit: "",
			want: "pde meio peito frango",
		},
		{
			name: "empty produces empty",
			in:   "",
			unit: "",
			want: "",
		},
		{
			name: "stopword-only produces empty",
			in:   "KG CAIXA EMB",
			unit: "",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeStem(tc.in, tc.unit, NewBrandDictionary(tc.brands))
			if got != tc.want {
				t.Errorf("NormalizeStem(%q,%q) = %q, want %q", tc.in, tc.unit, got, tc.want)
			}
		})
	}
}

func TestNormalizeStemAccents(t *testing.T) {
	// Pêra, maçã, limão spellings with accents collapse to the unaccented
	// stem, which is what commodity_synonyms.synonym stores.
	for in, want := range map[string]string{
		"PERA 1KG":         "pera",
		"Maçã Gala":        "maca gala",
		"LIMÃO TAHITI":     "limao tahiti",
		"Pão Francês":      "pao frances",
		"Cenoura Orgânica": "cenoura organica",
	} {
		if got := NormalizeStem(in, "", nil); got != want {
			t.Errorf("NormalizeStem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommodityKeyDeterministicAndUnitScoped(t *testing.T) {
	a := CommodityKey("limao tahiti", "UN")
	b := CommodityKey("limao tahiti", "UN")
	if a != b {
		t.Fatalf("deterministic key mismatch: %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Errorf("key length = %d, want 64 (sha256 hex)", len(a))
	}
	c := CommodityKey("limao tahiti", "KG")
	if a == c {
		t.Error("key must vary with unit")
	}
}
