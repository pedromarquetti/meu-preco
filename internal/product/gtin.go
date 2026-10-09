// Package product implements the identity tier: GTIN normalization and
// validation, unit normalization, commodity resolution and the pure
// classification decision that maps one receipt line to one of the five
// identity statuses (see schema.go). It has no database or network
// dependencies — lookups come through the domain repositories.
package product

import "strings"

// Brazilian GS1 manufacturer prefixes. Only 789/790 barcodes resolve through
// the national catalog; everything else is treated as un-barcoded.
const (
	gsiBrazilPrefixMin = "789"
	gsiBrazilPrefixMax = "790"
)

// NormalizeGTIN returns the GS1 14-digit zero-padded form of a valid GTIN-8,
// GTIN-12, GTIN-13 or GTIN-14 barcode whose check digit verifies and whose
// country prefix is Brazilian (789|790). The second return value reports
// whether the code is a valid Brazilian GTIN.
//
// This intentionally rejects: non-BR prefixes (they cannot resolve through our
// enrichment source), the 11-digit ABA/Banco do Brasil codes occasionally
// printed on coupons, and garbage.
func NormalizeGTIN(code string) (string, bool) {
	digits := barcodeDigits(code)
	if digits == "" {
		return "", false
	}
	// The stored 14-digit form may carry leading padding zeros (indicator
	// digit 0). Validate the meaningful barcode first, then re-inflate.
	meaningful := strings.TrimLeft(digits, "0")
	switch len(meaningful) {
	case 8, 12, 13, 14:
	default:
		return "", false
	}
	if !brazilianPrefix(meaningful) {
		return "", false
	}
	if !validCheckDigit(meaningful) {
		return "", false
	}
	return strings.Repeat("0", 14-len(meaningful)) + meaningful, true
}

// barcodeDigits extracts the digit payload of a printed barcode. It tolerates
// whitespace and printer separators but rejects any other character, so a
// stray letter invalidates the code instead of being silently ignored.
func barcodeDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '/':
			continue
		default:
			return ""
		}
	}
	return b.String()
}

// brazilianPrefix reports whether the GTIN starts with 789 or 790.
func brazilianPrefix(digits string) bool {
	if len(digits) < 3 {
		return false
	}
	p := digits[:3]
	return p >= gsiBrazilPrefixMin && p <= gsiBrazilPrefixMax
}

// validCheckDigit applies the GS1 modulus-10 check digit over the payload
// (all digits but the last), weighting from right to left 3,1,3,1,... .
func validCheckDigit(digits string) bool {
	n := len(digits)
	if n < 2 {
		return false
	}
	sum := 0
	weight := 3
	for i := n - 2; i >= 0; i-- {
		d := int(digits[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		sum += d * weight
		if weight == 3 {
			weight = 1
		} else {
			weight = 3
		}
	}
	check := (10 - sum%10) % 10
	return check == int(digits[n-1]-'0')
}
