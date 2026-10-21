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

// BrandDictionary is the set of curated brand names recognised in GTIN-less
// descriptions, so "QUEIJO BRIE TIROLEZ" reduces to the commodity "queijo
// brie" plus the brand "Tirolez". Multi-token brands ("tio joao", "3
// coracoes") are matched as contiguous phrases.
type BrandDictionary struct {
	phrases  map[string]string // normalized phrase -> display spelling
	maxWords int
}

// BrandEntry supplies the normalized lookup phrase and its display spelling.
// Keeping both avoids losing matches when a display name contains punctuation
// or a different accenting convention than the stored lookup key.
type BrandEntry struct {
	Key     string
	Display string
}

// NewBrandDictionary builds a dictionary from brand display strings. Each
// brand is normalized the same way a description is (lower-case,
// accent-stripped, tokenized); the normalized phrase keys the display spelling
// that is stamped onto a products.brand column.
func NewBrandDictionary(brands []string) *BrandDictionary {
	entries := make([]BrandEntry, 0, len(brands))
	for _, brand := range brands {
		entries = append(entries, BrandEntry{Key: brand, Display: brand})
	}

	return NewBrandDictionaryEntries(entries)
}

// NewBrandDictionaryEntries builds a dictionary from normalized brand keys and
// their display spellings.
func NewBrandDictionaryEntries(entries []BrandEntry) *BrandDictionary {
	d := &BrandDictionary{phrases: make(map[string]string, len(entries))}
	for _, entry := range entries {
		toks := normalizeStem(entry.Key)
		if len(toks) == 0 {
			continue
		}
		display := strings.TrimSpace(entry.Display)
		if display == "" {
			display = strings.TrimSpace(entry.Key)
		}
		d.phrases[strings.Join(toks, " ")] = display
		if len(toks) > d.maxWords {
			d.maxWords = len(toks)
		}
	}
	return d
}

// match finds the longest brand phrase appearing as a contiguous run in toks,
// returning the display spelling and the [start,end) token span. A nil
// dictionary (brands disabled) never matches.
func (d *BrandDictionary) match(toks []string) (display string, start, end int, ok bool) {
	if d == nil || d.maxWords == 0 {
		return "", 0, 0, false
	}
	for n := min(d.maxWords, len(toks)); n >= 1; n-- {
		for i := 0; i+n <= len(toks); i++ {
			if disp, found := d.phrases[strings.Join(toks[i:i+n], " ")]; found {
				return disp, i, i + n, true
			}
		}
	}
	return "", 0, 0, false
}

// ExtractBrand returns the curated brand display spelling found in description
// and true when a known brand phrase is present. It is the read-only twin of
// the brand filter NormalizeStem applies.
func ExtractBrand(description string, brands *BrandDictionary) (string, bool) {
	display, _, _, ok := brands.match(normalizeStem(description))
	return display, ok
}

// NormalizeStem turns a receipt description plus its printed unit into the
// commodity lookup stem: lower-case, accent-stripped, minus packaging tokens,
// minus the printed unit token, minus any known brand phrase, keeping a single
// space between words. Returns "" when nothing identifiable remains.
func NormalizeStem(description, unit string, brands *BrandDictionary) string {
	toks := normalizeStem(description)
	_, brandStart, brandEnd, hasBrand := brands.match(toks)

	var out []string
	for i, t := range toks {
		if hasBrand && i >= brandStart && i < brandEnd {
			continue
		}
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
// sha256(canonical_stem "|" canonical_unit ["|" brand]). Two receipts for the
// same commodity, unit and brand always collide on the same key, while the
// brand separates e.g. Tirolez brie from Vigor brie. A blank brand reproduces
// the brandless key, keeping existing produce identities stable.
func CommodityKey(canonicalStem, unit, brand string) string {
	seed := canonicalStem + "|" + unit
	if b := strings.Join(normalizeStem(brand), " "); b != "" {
		seed += "|" + b
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}
