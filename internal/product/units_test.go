package product

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestNormalizeUnit(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"KG", UnitKG, true},
		{"kg", UnitKG, true},
		{"KILO", UnitKG, true},
		{"gr", UnitGR, true},
		{"G", UnitGR, true},
		{"un", UnitUN, true},
		{"UNIDADE", UnitUN, true},
		{"PC", UnitUN, true},
		{"CAIXA", UnitUN, true}, // pack counted as single item
		{"dz", UnitDZ, true},
		{"ml", UnitML, true},
		{"LT", UnitLT, true},
		{"LITRO", UnitLT, true},
		{"", "", false},
		{"CXZZ", "", false},
		{"cada", "", false}, // "cada" is UN on some receipts but not mapped
		{"  kg  ", UnitKG, true},
	}
	for _, tc := range tests {
		got, ok := NormalizeUnit(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("NormalizeUnit(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPricePerUnit(t *testing.T) {
	// 0.815 KG, total 4.88 → ~5.9877 per KG (DoD arithmetic).
	qty := decimal.RequireFromString("0.815")
	total := decimal.RequireFromString("4.88")
	ppu, ok := PricePerUnit("KG", qty, total)
	if !ok {
		t.Fatal("PricePerUnit(KG) ok=false")
	}
	want := decimal.RequireFromString("5.9877300613496932515337423312883435582822086").Round(4)
	if !ppu.Round(4).Equal(want) {
		t.Errorf("per KG = %s, want ~%s", ppu.StringFixed(4), "5.9877")
	}

	// Unknown unit: no uniform basis.
	if _, ok := PricePerUnit("CADA", qty, total); ok {
		t.Error("PricePerUnit(CADA) ok=true")
	}
	// Zero quantity: division undefined.
	if _, ok := PricePerUnit("KG", decimal.Zero, total); ok {
		t.Error("PricePerUnit(qty=0) ok=true")
	}
}

func TestPricePerUnitDzKeepsDozen(t *testing.T) {
	// A dozen priced as batching unit: normalized_unit DZ, per-dozen price.
	qty := decimal.RequireFromString("2")
	total := decimal.RequireFromString("30")
	ppu, ok := PricePerUnit("DZ", qty, total)
	if !ok {
		t.Fatal("PricePerUnit(DZ) ok=false")
	}
	if !ppu.Equal(decimal.RequireFromString("15")) {
		t.Errorf("per DZ = %s, want 15", ppu.String())
	}
}
