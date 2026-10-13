package product

import (
	"strings"

	"github.com/shopspring/decimal"
)

// Normalized unit vocabulary. DZ stays DZ at this tier —
// collapse to UN happens later at aggregation time, where a dozen is exactly
// 12 pieces.
const (
	UnitKG = "KG"
	UnitUN = "UN"
	UnitDZ = "DZ"
	UnitGR = "GR"
	UnitML = "ML"
	UnitLT = "LT"
)

// unitAliases maps every printed unit spelling onto one canonical token.
// Keys are upper-cased; lookup normalizes the case first.
var unitAliases = map[string]string{
	// weight (kilo)
	"KG": UnitKG, "KILO": UnitKG, "KILOS": UnitKG, "KGS": UnitKG, "QUILO": UnitKG, "QUILOS": UnitKG, "KILOGRAMA": UnitKG,
	// gram
	"G": UnitGR, "GR": UnitGR, "GRM": UnitGR, "GRAMAS": UnitGR, "GRAMA": UnitGR,
	// single pieces / packs counted individually
	"UN": UnitUN, "UND": UnitUN, "UNID": UnitUN, "UNIDADE": UnitUN, "UNIDADES": UnitUN,
	"PC": UnitUN, "PCS": UnitUN, "PCT": UnitUN, "PCTA": UnitUN, "PACOTE": UnitUN, "PECA": UnitUN, "PEÇA": UnitUN,
	"CJ": UnitUN, "CX": UnitUN, "CXA": UnitUN, "CAIXA": UnitUN,
	// pieces that cross pack/count ambiguity: sold as single items
	"DIV": UnitUN, "DIVERSOS": UnitUN, "FARDO": UnitUN, "FRD": UnitUN,
	// dozen (count of 12)
	"DZ": UnitDZ, "DUZIA": UnitDZ, "DUZIAS": UnitDZ,
	// volume metric
	"ML": UnitML, "MILILITRO": UnitML, "MILILITROS": UnitML,
	// volume liter
	"L": UnitLT, "LT": UnitLT, "LITRO": UnitLT, "LITROS": UnitLT,
}

// NormalizeUnit maps a printed unit to its canonical token. The boolean is
// false when the unit is unknown (then price-per-unit cannot be computed and
// the line keeps its raw unit).
func NormalizeUnit(raw string) (string, bool) {
	key := strings.ToUpper(strings.TrimSpace(raw))
	u, ok := unitAliases[key]
	return u, ok
}

// PricePerUnit computes the uniform price-per-normalized-unit from the line's
// total price and quantity: price_per_unit = total / qty. Both totals are kept
// decimal; only the naive division is performed here (rounding happens at the
// storage boundary). It returns ok=false when the unit is unknown or qty <= 0,
// because then the arithmetic would be per a non-uniform basis.
func PricePerUnit(rawUnit string, qty, total decimal.Decimal) (decimal.Decimal, bool) {
	if _, ok := NormalizeUnit(rawUnit); !ok {
		return decimal.Zero, false
	}
	if qty.IsZero() || qty.IsNegative() {
		return decimal.Zero, false
	}
	return total.Div(qty), true
}

// ComparableUnit maps a normalized unit onto the aggregation basis: DZ
// collapses into UN (a dozen is exactly 12 pieces) so mixed-DZ/UN curves are
// comparable; every other normalized unit compares against itself.
func ComparableUnit(unit string) string {
	if unit == UnitDZ {
		return UnitUN
	}
	return unit
}

// ComparePricePerUnit converts a per-unit price to the aggregation basis: a
// per-dozen price divides by 12 so the DZ point joins the UN series at the
// per-piece rate. Non-DZ units are returned unchanged.
func ComparePricePerUnit(unit string, pricePerUnit decimal.Decimal) decimal.Decimal {
	if unit == UnitDZ {
		return pricePerUnit.Div(decimal.NewFromInt(12))
	}
	return pricePerUnit
}
