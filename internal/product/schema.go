package product

import (
	"context"
	"errors"
	"strings"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

// Identity statuses. These strings are the price_logs.identity_status CHECK
// values — keep them in sync with migration 000001.
const (
	StatusGtinKnown            = "gtin_known"
	StatusGtinNew              = "gtin_new"
	StatusCommodityFingerprint = "commodity_fingerprint"
	StatusUIDUncertain         = "uid_uncertain"
	StatusParseError           = "parse_error"
)

// Similarity thresholds for the fuzzy commodity tier. Above HighSimilarity an
// existing product is a confident fingerprint; between HighSimilarity and
// LowSimilarityFloor the match is suggestive and goes to review; below the
// floor it is uid_uncertain.
const (
	HighSimilarity     = 0.5
	LowSimilarityFloor = 0.3
)

// Line is the classified unit: a parsed NfceItem plus the printer's unit
// spelling, decoupled from the DB schema so the decision function stays pure.
type Line struct {
	Description string
	Code        string // the cProd that may be a GTIN
	Qty         decimal.Decimal
	Unit        string          // as printed
	TotalPrice  decimal.Decimal // Vl. Total (from which price-per-unit derives)
}

// Decision is the outcome of classifying one receipt line against the
// catalog. It is a pure value: consumers (the commit transaction)
// translate it into products/price_logs rows.
type Decision struct {
	Status          string
	GTIN            string // normalized 14-digit when the line had a valid BR GTIN
	ProductKey      string // key to persist into products (gtin_new/commodity_fingerprint)
	Name            string // display name to store for a new product
	Brand           string // brand to store for a new product
	CanonicalStem   string // canonical commodity stem to store for a new product
	Unit            string // canonical unit when known (else raw)
	PricePerUnit    decimal.Decimal
	HasPricePerUnit bool
	Similarity      float64 // only set along the fuzzy tier
	Message         string  // human explanation for review/ops
}

// Catalog is the minimal lookup surface the classifier needs. It is
// domain.ProductStore + domain.CommoditySynonymStore, stated explicitly so
// fakes in tests are trivial.
type Catalog interface {
	GetByGTIN(ctx context.Context, gtin string) (*domain.Product, error)
	GetByKey(ctx context.Context, key string) (*domain.Product, error)
	NearestMatch(ctx context.Context, stem string, threshold float64) (*domain.Product, float64, error)
	Canonicalize(ctx context.Context, synonym string) (*domain.CommoditySynonym, error)
	MatchPrefix(ctx context.Context, stem string) (*domain.CommoditySynonym, error)
}

// CheeseAliasSource marks synonym rows that are ambiguous standalone words
// ("mofo", "azul") and therefore only resolve after a cheese prefix.
const CheeseAliasSource = "cheese_alias"

// SynonymCanonicalizer is the narrow lookup surface needed to resolve cheese
// aliases; both Catalog and domain.CommoditySynonymStore satisfy it.
type SynonymCanonicalizer interface {
	Canonicalize(ctx context.Context, synonym string) (*domain.CommoditySynonym, error)
}

// cheesePrefix reports whether a matched prefix denotes cheese, either as the
// word itself ("queijo") or through its expansion ("qj"/"qjo" -> "queijo").
func cheesePrefix(prefix, expansion string) bool {
	if strings.EqualFold(strings.TrimSpace(prefix), "queijo") {
		return true
	}
	first, _, _ := strings.Cut(strings.TrimSpace(expansion), " ")
	return strings.EqualFold(first, "queijo")
}

// ResolveCheeseAliasRemainder maps the remainder of a cheese-prefixed stem
// ("queijo mofo azul", "qj mofo") onto a canonical commodity via the
// cheese_alias rows ("mofo"/"azul" -> "queijo gorgonzola"). It only resolves
// when the prefix/expansion denotes cheese and every remainder token is a
// cheese_alias row sharing one canonical name; otherwise it returns
// domain.ErrNotFound so the caller can fall through. Callers must only invoke
// it after a taxonomy prefix matched, which keeps ambiguous words such as
// "mofo" scoped to cheese descriptions.
func ResolveCheeseAliasRemainder(ctx context.Context, cat SynonymCanonicalizer,
	prefix, expansion, remainder string) (*domain.CommoditySynonym, error) {
	if !cheesePrefix(prefix, expansion) {
		return nil, domain.ErrNotFound
	}

	tokens := strings.Fields(remainder)
	if len(tokens) == 0 {
		return nil, domain.ErrNotFound
	}

	var resolved *domain.CommoditySynonym
	for _, token := range tokens {
		syn, err := cat.Canonicalize(ctx, token)
		if err != nil {
			return nil, err
		}
		if syn.Source != CheeseAliasSource {
			return nil, domain.ErrNotFound
		}
		if resolved == nil {
			resolved = syn
			continue
		}
		if syn.CanonicalName != resolved.CanonicalName {
			return nil, domain.ErrNotFound
		}
	}
	return resolved, nil
}

// remainderHasCheeseAlias reports whether any token in remainder is a
// cheese_alias row. It lets Classify refuse to mint a new commodity from a
// mixed phrase such as "queijo mofo prato".
func remainderHasCheeseAlias(ctx context.Context, cat SynonymCanonicalizer, remainder string) bool {
	for _, token := range strings.Fields(remainder) {
		if syn, err := cat.Canonicalize(ctx, token); err == nil && syn.Source == CheeseAliasSource {
			return true
		}
	}
	return false
}

func commodityDecision(ctx context.Context, cat Catalog, syn *domain.CommoditySynonym,
	canonicalStem, brand string, dec Decision) Decision {
	unit := syn.Unit
	if unit == "" {
		unit = dec.Unit
	}

	dec.Status = StatusCommodityFingerprint
	dec.ProductKey = CommodityKey(canonicalStem, unit, brand)
	dec.Name = canonicalStem
	dec.CanonicalStem = canonicalStem
	dec.Brand = brand
	dec.Unit = unit

	if p, err := cat.GetByKey(ctx, dec.ProductKey); err == nil {
		dec.Name = p.Name
		if b := deref(p.Brand); b != "" {
			dec.Brand = b
		}
		if s := deref(p.CanonicalStem); s != "" {
			dec.CanonicalStem = s
		}
	}
	return dec
}

// Classify is the pure Phase-4 decision function. It never writes, never does
// I/O beyond the injected catalog lookups, and maps every input line onto
// exactly one of the five identity statuses. brands (optional) carries the
// token dictionary extracted from the barcoded catalog so known brand names
// are dropped from commodity stems; nil disables that filter.
func Classify(ctx context.Context, cat Catalog, in Line, brands *BrandDictionary) Decision {
	decision := Decision{Status: StatusParseError}

	if strings.TrimSpace(in.Description) == "" {
		decision.Message = "empty description"
		return decision
	}
	if in.Qty.IsZero() || in.Qty.IsNegative() {
		decision.Message = "non-positive quantity"
		return decision
	}

	// Normalized unit / price per unit are valid for every non-garbage line,
	// independent of the identity tier that classifies it.
	if u, ok := NormalizeUnit(in.Unit); ok {
		decision.Unit = u
		if ppu, ok := PricePerUnit(in.Unit, in.Qty, in.TotalPrice); ok {
			decision.PricePerUnit = ppu
			decision.HasPricePerUnit = true
		}
	}

	// Tier 1 — barcoded goods.
	if gtin, ok := NormalizeGTIN(in.Code); ok {
		decision.GTIN = gtin
		if p, err := cat.GetByGTIN(ctx, gtin); err == nil {
			decision.Status = StatusGtinKnown
			decision.ProductKey = p.ProductKey
			decision.Name = p.Name
			decision.Brand = deref(p.Brand)
			return decision
		} else if errors.Is(err, domain.ErrNotFound) {
			decision.Status = StatusGtinNew
			decision.ProductKey = gtin
			decision.Name = strings.TrimSpace(in.Description)
			return decision
		}
		decision.Status = StatusUIDUncertain
		decision.Message = "catalog lookup failed"
		return decision
	}

	// Tier 2 — commodity goods (produce, bulk, cheeses). The brand (when the
	// description carries a curated one) is peeled off first: it scopes the
	// identity and is stored alongside the canonical name.
	brand, _ := ExtractBrand(in.Description, brands)
	stem := NormalizeStem(in.Description, in.Unit, brands)
	if stem == "" {
		decision.Status = StatusUIDUncertain
		decision.Message = "no identifiable stem in description"
		return decision
	}

	// Exact match first. Standalone cheese aliases ("mofo", "azul") are
	// intentionally skipped: they are only meaningful after a cheese prefix
	// (e.g. "qj mofo" -> "queijo gorgonzola").
	if syn, err := cat.Canonicalize(ctx, stem); err == nil {
		if syn.Source != CheeseAliasSource || syn.Synonym != stem {
			return commodityDecision(ctx, cat, syn, syn.CanonicalName, brand, decision)
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		decision.Status = StatusUIDUncertain
		decision.Message = "taxonomy lookup failed"
		return decision
	}

	// Abbreviation prefix expansion: "qjo prato" / "qjoprato" -> "queijo prato".
	if pfx, err := cat.MatchPrefix(ctx, stem); err == nil && pfx.PrefixExpansion != nil && strings.TrimSpace(*pfx.PrefixExpansion) != "" {
		expansion := strings.TrimSpace(*pfx.PrefixExpansion)
		remainder := strings.TrimSpace(stem[len(pfx.Synonym):])
		expanded := strings.TrimSpace(expansion + " " + remainder)

		if expanded != "" {
			if expanded != stem {
				if resolved, err := cat.Canonicalize(ctx, expanded); err == nil {
					// The expanded stem may be an alias (e.g. "queijo mofo" ->
					// "queijo gorgonzola"); use the row's canonical identity so
					// aliases collapse onto the same product key.
					return commodityDecision(ctx, cat, resolved, resolved.CanonicalName, brand, decision)
				} else if !errors.Is(err, domain.ErrNotFound) {
					decision.Status = StatusUIDUncertain
					decision.Message = "taxonomy lookup failed"
					return decision
				}
			}

			// Ambiguous cheese words ("mofo", "azul") collapse onto their
			// canonical alias only when every remainder token agrees. This also
			// covers identity prefixes ("queijo mofo"), where the expansion is
			// a no-op and must not short-circuit the alias lookup.
			if remSyn, err := ResolveCheeseAliasRemainder(ctx, cat, pfx.Synonym, expansion, remainder); err == nil {
				return commodityDecision(ctx, cat, remSyn, remSyn.CanonicalName, brand, decision)
			} else if !errors.Is(err, domain.ErrNotFound) {
				decision.Status = StatusUIDUncertain
				decision.Message = "taxonomy lookup failed"
				return decision
			}

			if expanded != stem {
				if cheesePrefix(pfx.Synonym, expansion) && remainderHasCheeseAlias(ctx, cat, remainder) {
					// Mixed alias + product phrase ("queijo mofo prato"): do
					// not invent a new commodity; fall through to fuzzy match.
				} else {
					// No exact row for the expanded identity: still accept the
					// reconstructed commodity using the prefix row's metadata.
					return commodityDecision(ctx, cat, pfx, expanded, brand, decision)
				}
			}
		}
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		decision.Status = StatusUIDUncertain
		decision.Message = "taxonomy prefix lookup failed"
		return decision
	}

	// Fuzzy nearest-match against the catalog.
	p, sim, err := cat.NearestMatch(ctx, stem, LowSimilarityFloor)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			decision.Status = StatusUIDUncertain
			decision.Message = "catalog nearest-match failed"
			return decision
		}
		decision.Status = StatusUIDUncertain
		decision.Message = "unknown product"
		return decision
	}
	decision.Similarity = sim
	if sim >= HighSimilarity {
		if p.ProductKey == "" {
			// A legacy/synthetic row with no key cannot back a price_log FK;
			// route it to review instead of minting a blank-key product.
			decision.Status = StatusUIDUncertain
			decision.Message = "nearest match has no product key"
			return decision
		}

		decision.Status = StatusCommodityFingerprint
		decision.ProductKey = p.ProductKey
		decision.Name = p.Name
		decision.Brand = deref(p.Brand)
		decision.CanonicalStem = deref(p.CanonicalStem)

		return decision
	}
	decision.Status = StatusUIDUncertain
	decision.Message = "low similarity to nearest match"
	return decision
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
