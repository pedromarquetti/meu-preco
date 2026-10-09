package product

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Pack/pezjo tokens removed from a description stem:
// packaging hints that carry no product identity.
var packTokens = map[string]bool{
	"EMB": true, "EMBALAGEM": true, "SACO": true, "SACOS": true,
	"CAIXA": true, "CAIXAS": true, "PET": true, "FARDO": true, "FARDA": true,
}

// unitTokens are the printed-unit spellings that are meaningless inside a
// commodity stem ("LIMAO TAHITI KG" → "limao tahiti").
var unitTokens = map[string]bool{}

func init() {
	for alias := range unitAliases {
		unitTokens[alias] = true
	}
}

// normalizeStem lower-cases a description, strips accents and collapses
// whitespace, returning the token stream used for brand and pack filtering.
func normalizeStem(s string) []string {
	decomposed, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn))), strings.ToLower(s))
	if err != nil {
		decomposed = strings.ToLower(s)
	}
	return strings.Fields(decomposed)
}

// BrandDictionary is the set of brand tokens extracted from the barcoded
// product catalog, so "ARROZ TIO JOAO" can be reduced to the commodity
// "arroz" when TIO JOAO is a known brand.
type BrandDictionary struct {
	tokens map[string]bool
}

// NewBrandDictionary builds a dictionary from brand strings. Each brand is
// normalized the same way a description is (lower-case, accent-stripped,
// tokenized) and every token is recorded.
func NewBrandDictionary(brands []string) *BrandDictionary {
	d := &BrandDictionary{tokens: make(map[string]bool)}
	for _, b := range brands {
		for _, tok := range normalizeStem(b) {
			d.tokens[tok] = true
		}
	}
	return d
}

// NormalizeStem turns a receipt description plus its printed unit into the
// commodity lookup stem: lower-case, accent-stripped, minus packaging tokens,
// minus the printed unit token, minus any known brand tokens, keeping a single
// space between words. Returns "" when nothing identifiable remains.
func NormalizeStem(description, unit string, brands *BrandDictionary) string {
	toks := normalizeStem(description)

	var out []string
	for _, t := range toks {
		u := strings.ToUpper(t)
		switch {
		case isNumeric(u):
			continue
		case packTokens[u]:
			continue
		case unitTokens[u]:
			continue
		case len(t) == 1:
			continue
		}
		// Glued quantity+unit ("5KG", "1LT", "500G") drops entirely; the pack
		// size is identity noise at the commodity tier.
		if gluedQuantityUnit(u) {
			continue
		}
		if brands != nil && brands.tokens[strings.ToLower(t)] {
			continue
		}
		out = append(out, t)
	}

	// The explicit unit often appears without a matching alias
	// ("LIMAO TAHITI KG" with unit "KG"): drop it too.
	if u, ok := NormalizeUnit(unit); ok {
		for i, t := range out {
			if t == strings.ToLower(u) {
				out = append(out[:i], out[i+1:]...)
				break
			}
		}
	}

	return strings.Join(out, " ")
}

func isNumeric(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return s != ""
}

// gluedQuantityUnit reports whether t is a quantity glued to a unit token
// ("5KG", "1LT", "500g") that carries no identity beyond pack size.
func gluedQuantityUnit(t string) bool {
	for i, r := range t {
		if !unicode.IsDigit(r) {
			return unicode.IsLetter(r) && unitTokens[strings.ToUpper(t[i:])]
		}
	}
	return false
}

// CommodityKey derives the deterministic product_key for a commodity identity:
// sha256(canonical_stem "|" canonical_unit). Two receipts for the same
// commodity and unit always collide on the same key, so the fingerprint tier
// can aggregate across stores.
func CommodityKey(canonicalStem, unit string) string {
	sum := sha256.Sum256([]byte(canonicalStem + "|" + unit))
	return hex.EncodeToString(sum[:])
}
