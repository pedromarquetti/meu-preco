// Package audit contains the product identity audit used by the operational
// repair command. It deliberately has no database knowledge.
package audit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

const (
	MissingStem       = "missing_canonical_stem"
	StemMismatch      = "canonical_stem_mismatch"
	KeyMismatch       = "product_key_mismatch"
	IdentityCollision = "identity_collision"
	// ManualReview marks a row whose display name normalizes to no identity
	// (packaging-only names such as "Saco"). The stored stem may be the only
	// meaningful identity, so a repair based on the name must not be automatic.
	ManualReview = "manual_review_required"
)

type Finding struct {
	Product       domain.Product
	ExpectedStem  string
	ExpectedKey   string
	Statuses      []string
	CollisionWith int64
	ManualReview  bool
}

func (f Finding) Drifted() bool { return len(f.Statuses) != 0 }

// Skip describes one row the repair run refused to touch.
type Skip struct {
	ID     int64
	Reason string
}

// ApplyResult reports what a repair run changed and which rows it refused to
// touch because their computed identity belongs to another product or needs
// operator review.
type ApplyResult struct {
	Repaired int
	Skipped  []Skip
}

// Analyze classifies every commodity product against the identity that its
// current presentation fields imply. Barcode products are intentionally skipped:
// their product_key is the GTIN, not a commodity hash, so rewriting it would
// break barcode matching and allow a duplicate row on the next receipt.
//
// Collisions are calculated against the computed key so no repair can silently
// take another product's identity.
func Analyze(products []domain.Product, brands *product.BrandDictionary) []Finding {
	ordered := append([]domain.Product(nil), products...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	storedOwners := make(map[string]int64, len(ordered))
	expectedOwners := make(map[string]int64, len(ordered))
	for _, p := range ordered {
		if isBarcodeProduct(p) {
			continue
		}
		if p.ProductKey != "" {
			storedOwners[p.ProductKey] = p.ID
		}
		_, key := expectedIdentity(p, brands)
		if _, exists := expectedOwners[key]; !exists {
			expectedOwners[key] = p.ID
		}
	}

	out := make([]Finding, 0, len(ordered))
	for _, p := range ordered {
		if isBarcodeProduct(p) {
			// A GTIN key is a valid identity on its own; canonical_stem is
			// optional for those rows and must never be replaced with a hash.
			out = append(out, Finding{Product: p})
			continue
		}

		stem, key := expectedIdentity(p, brands)
		statuses := make([]string, 0, 3)
		storedStem := strings.TrimSpace(deref(p.CanonicalStem))
		if storedStem == "" {
			statuses = append(statuses, MissingStem)
		} else if storedStem != stem {
			statuses = append(statuses, StemMismatch)
		}

		// The plan's category is specifically "product_key does not match
		// canonical_stem|unit|brand". When the stem is missing there is no
		// stored identity to compare against, so the expected key is the only
		// valid commodity key.
		unit := canonicalUnit(p.Unit)
		var storedIdentityKey string
		if storedStem == "" {
			storedIdentityKey = key
		} else {
			storedIdentityKey = product.CommodityKey(storedStem, unit, deref(p.Brand))
		}
		if p.ProductKey != storedIdentityKey {
			statuses = append(statuses, KeyMismatch)
		}

		collision := int64(0)
		// A clean row already owns the computed key in the database. Only a
		// different row repairing onto that key collides with it. Without a
		// stored owner, two drifted rows still collide with each other; the
		// lowest id is treated as the owner and the rest are skipped.
		if owner, ok := storedOwners[key]; ok {
			if owner != p.ID {
				collision = owner
			}
		} else if owner, ok := expectedOwners[key]; ok && owner != p.ID {
			collision = owner
		}
		if collision != 0 {
			statuses = append(statuses, IdentityCollision)
		}

		// A drifted row whose packaging-only name carries no identity signal
		// needs an operator: the stored stem may be correct (or the name may
		// be wrong), so the repair command must not rewrite matching
		// semantics. A row that already agrees with its fallback identity is
		// left alone instead of being reported as permanent drift.
		manual := len(statuses) != 0 && product.NormalizeStem(p.Name, canonicalUnit(p.Unit), brands) == ""
		if manual {
			statuses = append(statuses, ManualReview)
		}

		out = append(out, Finding{
			Product:       p,
			ExpectedStem:  stem,
			ExpectedKey:   key,
			Statuses:      statuses,
			CollisionWith: collision,
			ManualReview:  manual,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Product.ID < out[j].Product.ID })
	return out
}

// Apply repairs only non-colliding commodity findings. Each product is updated
// in its own transaction, so an individual conflict or transient failure cannot
// partially change a row. Rows skipped because their key belongs to another
// product are reported back to the caller.
func Apply(ctx context.Context, stores domain.Stores, findings []Finding) (ApplyResult, error) {
	var result ApplyResult

	for _, finding := range findings {
		if !finding.Drifted() {
			continue
		}
		if finding.ManualReview {
			result.Skipped = append(result.Skipped, Skip{
				ID:     finding.Product.ID,
				Reason: "display name carries no identity; review the stored stem manually",
			})
			continue
		}
		if finding.CollisionWith != 0 {
			result.Skipped = append(result.Skipped, Skip{
				ID:     finding.Product.ID,
				Reason: fmt.Sprintf("computed identity belongs to product %d", finding.CollisionWith),
			})
			continue
		}

		err := stores.Tx(ctx, func(tx domain.Stores) error {
			return tx.Products().UpdateIdentity(ctx, finding.Product.ID, finding.ExpectedStem, finding.ExpectedKey)
		})
		switch {
		case err == nil:
			result.Repaired++
		case errors.Is(err, domain.ErrConflict):
			// A concurrent writer took the key after the audit snapshot:
			// leave the row for operator review instead of aborting the batch.
			result.Skipped = append(result.Skipped, Skip{
				ID:     finding.Product.ID,
				Reason: "computed identity was taken by a concurrent writer",
			})
		default:
			return result, fmt.Errorf("repair product %d: %w", finding.Product.ID, err)
		}
	}
	return result, nil
}

// isBarcodeProduct reports whether the row is identified by a GTIN rather than
// a commodity hash. GTIN rows are excluded from identity repair.
func isBarcodeProduct(p domain.Product) bool {
	return product.IsBarcodeIdentity(p.ProductKey, p.GTIN)
}

func expectedIdentity(p domain.Product, brands *product.BrandDictionary) (string, string) {
	return product.CanonicalIdentity(p.Name, deref(p.Brand), p.Unit, brands)
}

func canonicalUnit(unit string) string {
	if u, ok := product.NormalizeUnit(unit); ok {
		return u
	}
	return strings.TrimSpace(unit)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
