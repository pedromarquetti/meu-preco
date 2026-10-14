package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

// Resolution actions.
const (
	// ActionSkip is the explicit opt-out: the line writes nothing.
	ActionSkip = "skip"
	// ActionNeedsReview is the default for an unresolved uid_uncertain line:
	// it persists a private row with no product so an admin can resolve it.
	ActionNeedsReview = "needs_review"
	// ActionAssign attaches an existing product_id to the line.
	ActionAssign = "assign"
)

// CommitPreview implements the commit half of the two-phase commit: it
// validates the preview and, when confirm is true, applies the plan inside a
// single database transaction — auto-saving barcoded/commodity lines and
// honoring per-line uid_uncertain resolutions (needs_review | skip | assign).
// A uid_uncertain line with no resolution defaults to needs_review; skip must
// be requested explicitly. It then upserts the supermarket by CNPJ and the
// receipt by access key, inserting
// price_logs (UNIQUE(nfce_id, line) makes duplicates no-ops) and finally
// flipping the preview to committed. Any error rolls the whole unit back.
//
// The receipt is re-fetched at commit time through the resolver: the preview
// row only carries the plan (no DDL for a receipt snapshot), so the
// nfces/supermarkets rows are derived from a fresh normalized document.
//
// confirm=false is a dry run: it returns the commit summary without writing.
func (s *Service) CommitPreview(ctx context.Context, req CommitRequest) (*CommitResult, error) {
	if req.PreviewID == 0 {
		return nil, fmt.Errorf("%w: preview_id is required", ErrInvalidRequest)
	}

	preview, err := s.Stores.Previews().GetByID(ctx, req.PreviewID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrPreviewNotFound
		}
		return nil, err
	}
	if preview.UserID != req.UserID {
		// Indistinguishable from missing on purpose: a preview is an
		// ingestion plan a user owns, not browsable data.
		return nil, ErrPreviewNotFound
	}
	if preview.Status == "committed" {
		return nil, ErrAlreadyCommitted
	}
	if s.now().After(preview.TTL) {
		return nil, ErrPreviewExpired
	}

	// This preview is live; sweep anyone else's stale rows before writing.
	s.gc(ctx)

	plan, _, err := decodePlan(preview)
	if err != nil {
		return nil, err
	}

	res := &CommitResult{
		PreviewID: preview.ID,
		Confirmed: req.Confirm,
		AccessKey: preview.AccessKey,
	}
	if !req.Confirm {
		return res, nil
	}

	resolutions := indexResolutions(req.Resolutions)
	if err := validateResolutions(plan, resolutions); err != nil {
		return nil, err
	}

	// Re-fetch the receipt so the shared rows are written from the current
	// normalized document for the same access key the preview was built on.
	receipt, err := s.resolveReceipt(ctx, preview.AccessKey, preview.QRPayload)
	if err != nil {
		return nil, err
	}

	err = s.Stores.Tx(ctx, func(tx domain.Stores) error {
		sm := &domain.Supermarket{
			CNPJ:      receipt.Supermarket.CNPJ,
			LegalName: receipt.Supermarket.LegalName,
			Address:   receipt.Supermarket.Address,
		}
		if err := tx.Supermarkets().Upsert(ctx, sm); err != nil {
			return err
		}

		nf := &domain.Nfce{
			AccessKey:     receipt.AccessKey,
			Supermarket:   *sm,
			Number:        receipt.Number,
			Series:        receipt.Series,
			IssuedAt:      receipt.IssuedAt,
			Protocol:      receipt.Protocol,
			Environment:   receipt.Environment,
			Total:         receipt.Total,
			Discount:      receipt.Discount,
			PaidValue:     receipt.PaidValue,
			PaymentMethod: receipt.PaymentMethod,
			COO:           receipt.COO,
			PayloadRaw:    receipt.PayloadRaw,
			CreatedBy:     &req.UserID,
		}
		if err := tx.Nfces().Create(ctx, nf); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				// Already ingested (earlier commit or concurrent one): reuse
				// the existing row so the re-submit is a no-op.
				existing, err2 := tx.Nfces().GetByAccessKey(ctx, receipt.AccessKey)
				if err2 != nil {
					return err2
				}
				nf = existing
			} else {
				return err
			}
		}
		res.NfceID = nf.ID

		items := byLine(receipt.Items)

		for i := range plan {
			pi := &plan[i]
			item, ok := items[pi.Line]
			if !ok {
				// The re-fetched receipt changed since the preview; the plan
				// is the source of identity truth, but without amounts the
				// line cannot be persisted — skip it.
				res.Skipped++
				continue
			}

			productID, needsReview, err := resolveLineProduct(ctx, tx, req, pi, resolutions)
			if err != nil {
				return err
			}
			if productID == nil && !needsReview {
				// uid_uncertain explicitly resolved to "skip" (or a
				// parse_error line, which never writes).
				res.Skipped++
				continue
			}

			log := &domain.PriceLog{
				NfceID:              nf.ID,
				ProductID:           productID,
				Line:                pi.Line,
				DescriptionSnapshot: item.Description,
				StoreCode:           item.Code,
				Qty:                 item.Qty,
				Unit:                item.Unit,
				NormalizedUnit:      unitPtr(pi.Unit),
				PricePerUnit:        ppuFor(pi),
				UnitPrice:           item.UnitPrice,
				TotalPrice:          item.TotalPrice,
				IdentityStatus:      pi.Status,
				NeedsReview:         needsReview,
			}
			if needsReview {
				note := ActionNeedsReview
				log.ResolutionNote = &note
			} else if productID != nil && pi.Status == product.StatusUIDUncertain {
				note := ActionAssign
				log.ResolvedBy = &req.UserID
				log.ResolutionNote = &note
			}

			if err := tx.PriceLogs().Create(ctx, log); err != nil {
				if errors.Is(err, domain.ErrConflict) {
					// (nfce_id, line) already logged — the no-op the "re-send
					// the same access_key" requirement promises.
					res.Skipped++
					continue
				}
				return err
			}

			res.Written++
			switch {
			case needsReview:
				res.Review++
			case pi.Status == product.StatusUIDUncertain:
				res.Assigned++
			default:
				res.AutoSaved++
			}
		}

		if err := tx.Previews().MarkCommitted(ctx, preview.ID); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				return ErrAlreadyCommitted
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return res, nil
}

// resolveLineProduct decides what a plan line writes. For auto-save statuses
// it ensures the products row exists (creating gtin_new / commodity_fingerprint
// identities inside the tx) and returns its id. For uid_uncertain it applies
// the line's resolution, defaulting to needs_review when none was submitted.
// parse_error lines and an explicit "skip" produce no row (nil id,
// needsReview=false).
func resolveLineProduct(ctx context.Context, tx domain.Stores, req CommitRequest, pi *PlanItem, resolutions map[int]Resolution) (productID *int64, needsReview bool, err error) {
	switch pi.Status {
	case product.StatusGtinKnown, product.StatusGtinNew, product.StatusCommodityFingerprint:
		id, err := ensureProduct(ctx, tx, pi)
		if err != nil {
			return nil, false, err
		}

		return &id, false, nil

	case product.StatusUIDUncertain:
		r := resolutions[pi.Line]
		if r.Action == "" {
			r.Action = ActionNeedsReview // defaults to needs_review
		}

		switch r.Action {
		case ActionSkip:
			return nil, false, nil
		case ActionNeedsReview:
			return nil, true, nil
		case ActionAssign:
			if _, err := tx.Products().GetByID(ctx, r.ProductID); err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return nil, false, fmt.Errorf("%w: product %d does not exist", ErrInvalidResolution, r.ProductID)
				}
				return nil, false, err
			}
			id := r.ProductID
			return &id, false, nil
		default:
			return nil, false, fmt.Errorf("%w: unknown action %q", ErrInvalidResolution, r.Action)
		}
	default: // parse_error
		return nil, false, nil
	}
}

// ensureProduct returns the products row id backing a plan line, creating the
// master row when the identity does not exist yet (gtin_new / first-seen
// commodity fingerprint). Callers must run inside the commit transaction.
func ensureProduct(ctx context.Context, tx domain.Stores, pi *PlanItem) (int64, error) {
	if p, err := tx.Products().GetByKey(ctx, pi.ProductKey); err == nil {
		return p.ID, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return 0, err
	}

	p := &domain.Product{
		ProductKey: pi.ProductKey,
		Name:       pi.ProductName,
		Unit:       pi.Unit,
	}
	if pi.GTIN != "" {
		gtin := pi.GTIN
		p.GTIN = &gtin
	}
	if pi.Brand != "" {
		brand := pi.Brand
		p.Brand = &brand
	}

	if err := tx.Products().Create(ctx, p); err != nil {
		return 0, err
	}

	return p.ID, nil
}

// indexResolutions normalizes the resolutions array into a per-line map,
// collapsing duplicate lines (last one wins). An entry without an action
// defaults to needs_review, mirroring the unresolved-line default.
func indexResolutions(res []Resolution) map[int]Resolution {
	m := make(map[int]Resolution, len(res))
	for _, r := range res {
		if r.Line <= 0 {
			continue
		}

		if r.Action == "" {
			r.Action = ActionNeedsReview
		}

		m[r.Line] = r
	}
	return m
}

// validateResolutions rejects resolutions that cannot be honored: unknown
// actions, assign without a product_id, or a resolution aimed at a line the
// plan does not flag uid_uncertain.
func validateResolutions(plan []PlanItem, resolutions map[int]Resolution) error {
	byLine := make(map[int]PlanItem, len(plan))
	for _, pi := range plan {
		byLine[pi.Line] = pi
	}

	for line, r := range resolutions {
		pi, ok := byLine[line]
		if !ok {
			return fmt.Errorf("%w: line %d is not in the plan", ErrInvalidResolution, line)
		}
		if pi.Status != product.StatusUIDUncertain {
			return fmt.Errorf("%w: line %d does not need a resolution", ErrInvalidResolution, line)
		}
		switch r.Action {
		case ActionSkip, ActionNeedsReview:
		case ActionAssign:
			if r.ProductID <= 0 {
				return fmt.Errorf("%w: assign requires product_id", ErrInvalidResolution)
			}
		default:
			return fmt.Errorf("%w: unknown action %q", ErrInvalidResolution, r.Action)
		}
	}
	return nil
}

// byLine indexes receipt items for plan-line correlation.
func byLine(items []domain.NfceItem) map[int]domain.NfceItem {
	m := make(map[int]domain.NfceItem, len(items))
	for _, it := range items {
		m[it.Line] = it
	}
	return m
}

// unitPtr converts a canonical unit string into a nullable column value.
func unitPtr(u string) *string {
	if u == "" {
		return nil
	}
	return &u
}

// ppuFor renders a plan line's stored price-per-unit back into a decimal.
func ppuFor(pi *PlanItem) *decimal.Decimal {
	if pi.PricePerUnit == "" {
		return nil
	}
	d, err := decimal.NewFromString(pi.PricePerUnit)
	if err != nil {
		return nil
	}
	return &d
}
