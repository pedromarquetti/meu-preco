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
// catalog. It is a pure value: consumers (the Phase 5 commit transaction)
// translate it into products/price_logs rows.
type Decision struct {
	Status          string
	GTIN            string // normalized 14-digit when the line had a valid BR GTIN
	ProductKey      string // key to persist into products (gtin_new/commodity_fingerprint)
	Name            string // display name to store for a new product
	Brand           string // brand to store for a new product
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
}

// Classify is the pure Phase-4 decision function. It never writes, never does
// I/O beyond the injected catalog lookups, and maps every input line onto
// exactly one of the five identity statuses. brands (optional) carries the
// token dictionary extracted from the barcoded catalog so known brand names
// are dropped from commodity stems; nil disables that filter.
func Classify(ctx context.Context, cat Catalog, in Line, brands *BrandDictionary) Decision {
	dec := Decision{Status: StatusParseError}

	if strings.TrimSpace(in.Description) == "" {
		dec.Message = "empty description"
		return dec
	}
	if in.Qty.IsZero() || in.Qty.IsNegative() {
		dec.Message = "non-positive quantity"
		return dec
	}

	// Normalized unit / price per unit are valid for every non-garbage line,
	// independent of the identity tier that classifies it.
	if u, ok := NormalizeUnit(in.Unit); ok {
		dec.Unit = u
		if ppu, ok := PricePerUnit(in.Unit, in.Qty, in.TotalPrice); ok {
			dec.PricePerUnit = ppu
			dec.HasPricePerUnit = true
		}
	}

	// Tier 1 — barcoded goods.
	if gtin, ok := NormalizeGTIN(in.Code); ok {
		dec.GTIN = gtin
		if p, err := cat.GetByGTIN(ctx, gtin); err == nil {
			dec.Status = StatusGtinKnown
			dec.ProductKey = p.ProductKey
			dec.Name = p.Name
			dec.Brand = deref(p.Brand)
			return dec
		} else if errors.Is(err, domain.ErrNotFound) {
			dec.Status = StatusGtinNew
			dec.ProductKey = gtin
			dec.Name = strings.TrimSpace(in.Description)
			return dec
		}
		dec.Status = StatusUIDUncertain
		dec.Message = "catalog lookup failed"
		return dec
	}

	// Tier 2 — commodity goods (produce, bulk, cheeses).
	stem := NormalizeStem(in.Description, in.Unit, brands)
	if stem == "" {
		dec.Status = StatusUIDUncertain
		dec.Message = "no identifiable stem in description"
		return dec
	}

	if syn, err := cat.Canonicalize(ctx, stem); err == nil {
		key := CommodityKey(syn.CanonicalName, syn.Unit)
		dec.Status = StatusCommodityFingerprint
		dec.ProductKey = key
		dec.Name = syn.CanonicalName
		if p, err := cat.GetByKey(ctx, key); err == nil {
			dec.Name = p.Name
			dec.Brand = deref(p.Brand)
		}
		return dec
	} else if !errors.Is(err, domain.ErrNotFound) {
		dec.Status = StatusUIDUncertain
		dec.Message = "taxonomy lookup failed"
		return dec
	}

	// Fuzzy nearest-match against the catalog.
	p, sim, err := cat.NearestMatch(ctx, stem, LowSimilarityFloor)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			dec.Status = StatusUIDUncertain
			dec.Message = "catalog nearest-match failed"
			return dec
		}
		dec.Status = StatusUIDUncertain
		dec.Message = "unknown product"
		return dec
	}
	dec.Similarity = sim
	if sim >= HighSimilarity {
		dec.Status = StatusCommodityFingerprint
		dec.Name = p.Name
		dec.Brand = deref(p.Brand)
		return dec
	}
	dec.Status = StatusUIDUncertain
	dec.Message = "low similarity to nearest match"
	return dec
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
