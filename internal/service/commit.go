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
// be requested explicitly. An assign links the proposed product but persists
// the row with needs_review=true — assignments are proposals until an admin
// explicitly approves them. It then upserts the supermarket by CNPJ and the
// receipt by access key, inserting
// price_logs (UNIQUE(nfce_id, line) makes duplicates no-ops) and finally
// flipping the preview to committed. Any error rolls the whole unit back.
//
// Commit is database-only: the draft step persisted the normalized receipt
// snapshot on the previews row, so nfces/supermarkets/price_logs are derived
// from that snapshot without contacting SEFAZ again. A missing, corrupt, or
// mismatched snapshot fails explicitly; it never falls back to the resolver.
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

	// Use the persisted receipt snapshot to avoid re-fetching from the portal during commit.
	var receipt *domain.Nfce
	if len(preview.ReceiptSnapshot) > 0 {
		receipt, err = decodeReceiptSnapshot(preview.ReceiptSnapshot)
		if err != nil {
			s.Log.Error("failed to decode receipt snapshot", "preview_id", preview.ID, "err", err)
			return nil, err
		}

		// Validate that the snapshot belongs to this preview's access key.
		if receipt.AccessKey != preview.AccessKey {
			s.Log.Error("receipt snapshot access key mismatch", "preview_id", preview.ID,
				"preview_access_key", preview.AccessKey, "snapshot_access_key", receipt.AccessKey)
			return nil, fmt.Errorf("snapshot mismatch: expected %q, got %q", preview.AccessKey, receipt.AccessKey)
		}
	} else {
		// A missing snapshot is not an invitation to re-resolve the receipt:
		// callers must surface a typed error so legacy/corrupt drafts fail explicitly.
		return nil, ErrReceiptSnapshotMissing
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
				// The snapshot has no matching line (or an older snapshot was
				// hand-edited); the plan is the source of identity truth, but
				// without amounts the line cannot be persisted — skip it.
				res.Skipped++
				continue
			}

			productID, needsReview, action, err := resolveLineProduct(ctx, tx, req, pi, resolutions)
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
			switch action {
			case ActionAssign:
				note := ActionAssign
				log.ResolvedBy = &req.UserID
				log.ResolutionNote = &note
			case ActionNeedsReview:
				note := ActionNeedsReview
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

			if action == ActionAssign {
				// The price log id exists now; record the immutable proposal in
				// the same transaction. An uncertain line has no prior product,
				// so from_product_id stays NULL.
				event := &domain.PriceLogEvent{
					PriceLogID:  log.ID,
					ToProductID: productID,
					UserID:      req.UserID,
					Action:      ActionAssign,
				}
				if err := tx.Events().Create(ctx, event); err != nil {
					return err
				}
			}

			res.Written++
			switch {
			case action == ActionAssign:
				res.Assigned++
				res.PendingReview++
			case needsReview:
				res.Review++
				res.PendingReview++
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
// An assign always returns needsReview=true: a product-linked proposal is not
// public until an admin explicitly approves it. parse_error lines and an
// explicit "skip" produce no row (nil id, needsReview=false).
func resolveLineProduct(ctx context.Context, tx domain.Stores, req CommitRequest, pi *PlanItem, resolutions map[int]Resolution) (productID *int64, needsReview bool, action string, err error) {
	switch pi.Status {
	case product.StatusGtinKnown, product.StatusGtinNew, product.StatusCommodityFingerprint:
		id, err := ensureProduct(ctx, tx, pi)
		if err != nil {
			return nil, false, "", err
		}

		return &id, false, "", nil

	case product.StatusUIDUncertain:
		r := resolutions[pi.Line]
		if r.Action == "" {
			r.Action = ActionNeedsReview // defaults to needs_review
		}

		switch r.Action {
		case ActionSkip:
			return nil, false, ActionSkip, nil
		case ActionNeedsReview:
			return nil, true, ActionNeedsReview, nil
		case ActionAssign:
			if _, err := tx.Products().GetByIDForWrite(ctx, r.ProductID); err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return nil, false, "", fmt.Errorf("%w: product %d does not exist", ErrInvalidResolution, r.ProductID)
				}
				return nil, false, "", err
			}
			id := r.ProductID
			// An assignment is always a proposal, regardless of the caller's
			// role: only the explicit admin approval endpoint publishes it.
			return &id, true, ActionAssign, nil
		default:
			return nil, false, "", fmt.Errorf("%w: unknown action %q", ErrInvalidResolution, r.Action)
		}
	default: // parse_error
		return nil, false, "", nil
	}
}

// ensureProduct returns the products row id backing a plan line, creating the
// master row when the identity does not exist yet (gtin_new / first-seen
// commodity fingerprint). Callers must run inside the commit transaction.
func ensureProduct(ctx context.Context, tx domain.Stores, planItem *PlanItem) (int64, error) {
	if p, err := tx.Products().GetByKey(ctx, planItem.ProductKey); err == nil {
		return p.ID, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return 0, err
	}

	p := &domain.Product{
		ProductKey: planItem.ProductKey,
		Name:       planItem.ProductName,
		Unit:       planItem.Unit,
	}
	if planItem.GTIN != "" {
		gtin := planItem.GTIN
		p.GTIN = &gtin
	}
	if planItem.Brand != "" {
		brand := planItem.Brand
		p.Brand = &brand
	}

	if planItem.CanonicalStem != "" {
		stem := planItem.CanonicalStem
		p.CanonicalStem = &stem
	}

	if _, err := tx.Products().Create(ctx, p); err != nil {
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
