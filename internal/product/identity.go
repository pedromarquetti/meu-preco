package product

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// CanonicalIdentity computes the identity stored for a commodity product.
// The fallback keeps packaging-only names deterministic, matching the admin
// create path's legacy behavior.
func CanonicalIdentity(name, brand, unit string, brands *BrandDictionary) (stem, key string) {
	canonicalUnit, ok := NormalizeUnit(unit)
	if !ok {
		canonicalUnit = strings.TrimSpace(unit)
	}
	name = strings.TrimSpace(name)
	brand = strings.TrimSpace(brand)
	stem = NormalizeStem(name, canonicalUnit, brands)
	if stem == "" {
		stem = stableIdentityFallback(name, brand, canonicalUnit)
	}
	return stem, CommodityKey(stem, canonicalUnit, brand)
}

func stableIdentityFallback(name, brand, unit string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{name, brand, unit}, "|")))
	return hex.EncodeToString(sum[:])
}

// IsBarcodeIdentity reports whether a product is identified by a GTIN rather
// than a commodity hash. Barcode identities are immutable on rename: rewriting
// product_key from the barcode to a commodity hash would let the next receipt
// with that barcode create a duplicate row.
func IsBarcodeIdentity(productKey string, gtin *string) bool {
	if gtin != nil && strings.TrimSpace(*gtin) != "" {
		return true
	}
	return IsGTINKey(productKey)
}

// IsGTINKey reports whether key is a 14-digit numeric GTIN key.
func IsGTINKey(key string) bool {
	if len(key) != 14 {
		return false
	}
	for _, r := range key {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
