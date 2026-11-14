package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

// Admin-only curation: the needs-review queue, batch assigning to
// existing or brand-new products, product/supermarket merges and deletes, and
// user role management. Every mutation lives behind a single Stores.Tx so a
// failed batch never leaves a half-applied plan.

// ReviewProductView is the product a user proposed for an unresolved receipt
// line. It is only exposed through the admin queue.
type ReviewProductView struct {
	ID            int64   `json:"id"`
	ProductKey    string  `json:"product_key,omitempty"`
	GTIN          *string `json:"gtin,omitempty"`
	Name          string  `json:"name"`
	Brand         *string `json:"brand"`
	Unit          string  `json:"unit"`
	CanonicalStem *string `json:"canonical_stem,omitempty"`
}

// Proposal status describes the current proposal decision, while NeedsReview
// remains the materialized flag that controls whether the row is published and
// appears in the queue. A rejected proposal therefore remains in the queue.
const (
	ProposalStatusPending  = "pending"
	ProposalStatusRejected = "rejected"
)

// ReviewItemView is one queue row: the raw line plus the store context an admin
// needs to curate it, with a suggested canonical stem for the create path.
type ReviewItemView struct {
	PriceLogID       int64              `json:"price_log_id"`
	NfceID           int64              `json:"nfce_id"`
	AccessKey        string             `json:"access_key"`
	Line             int                `json:"line"`
	Description      string             `json:"description"`
	StoreCode        string             `json:"store_code"`
	CNPJBase         string             `json:"cnpj_base"`
	StoreName        string             `json:"store_name"`
	Unit             string             `json:"unit"`
	Qty              string             `json:"qty"`
	PricePerUnit     *string            `json:"price_per_unit,omitempty"`
	UnitPrice        string             `json:"unit_price"`
	TotalPrice       string             `json:"total_price"`
	SuggestedStem    string             `json:"suggested_stem"`
	ProposedProduct  *ReviewProductView `json:"proposed_product,omitempty"`
	ResolvedBy       *int64             `json:"resolved_by,omitempty"`
	ResolutionNote   *string            `json:"resolution_note,omitempty"`
	NeedsReview      bool               `json:"needs_review"`
	ProposalStatus   string             `json:"proposal_status"`
	LastReviewAction *string            `json:"last_review_action,omitempty"`
	CreatedAt        time.Time          `json:"created_at"`
}

// ReviewQueuePage is the paginated needs-review queue.
type ReviewQueuePage struct {
	Items []ReviewItemView `json:"items"`
	Page  Page             `json:"page"`
}

// Assignment is one line of a batch-assign request: point an existing
// price_log (its id/line) at a product via action "existing", or let the admin
// create the product on the fly via action "create" (name/brand/unit).
type Assignment struct {
	ID        int64  `json:"id"` // price_log_id
	Action    string `json:"action"`
	ProductID int64  `json:"product_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Brand     string `json:"brand,omitempty"`
	Unit      string `json:"unit,omitempty"`
}

// BatchAssignRequest is the wire body of POST /admin/products/batch-assign.
type BatchAssignRequest struct {
	Assignments []Assignment `json:"assignments"`
}

// BatchAssignResult reports the atomic outcome of a batch. Assigned counts
// proposals updated (the rows stay in the review queue until approved);
// Created counts products inserted on the fly.
type BatchAssignResult struct {
	Assigned int64 `json:"assigned"`
	Created  int64 `json:"created"`
}

// MergeResult reports one product merge.
type MergeResult struct {
	FromID     int64 `json:"from_id"`
	ToID       int64 `json:"to_id"`
	LogsMoved  int64 `json:"logs_moved"`
	DupRemoved bool  `json:"dup_removed"`
}

// MergeSupermarketsResult reports one emitter merge.
type MergeSupermarketsResult struct {
	FromID     int64 `json:"from_id"`
	ToID       int64 `json:"to_id"`
	NfcesMoved int64 `json:"nfces_moved"`
	DupRemoved bool  `json:"dup_removed"`
}

// DeleteProductResult reports a guarded product delete.
type DeleteProductResult struct {
	ID          int64 `json:"id"`
	LogsDeleted int64 `json:"logs_deleted"`
}

// UserView is the admin users-list projection (never exposes password_hash).
type UserView struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// UserPage is the paginated user list.
type UserPage struct {
	Items []UserView `json:"items"`
	Page  Page       `json:"page"`
}

// NeedsReviewQueue implements GET /admin/products/needs-review. The suggested
// canonical stem is derived from the raw line through the produce taxonomy;
// when the taxonomy has no entry the stem itself is the best guess.
func (s *Service) NeedsReviewQueue(ctx context.Context, page, limit int) (*ReviewQueuePage, error) {
	if err := validatePage(page, limit); err != nil {
		return nil, err
	}
	items, total, err := s.Stores.PriceLogs().NeedsReview(ctx, limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}

	out := make([]ReviewItemView, 0, len(items))

	brands, err := brandDictionary(ctx, s.Stores)
	if err != nil {
		return nil, err
	}

	for _, it := range items {
		v := ReviewItemView{
			PriceLogID:       it.PriceLogID,
			NfceID:           it.NfceID,
			AccessKey:        it.AccessKey,
			Line:             it.Line,
			Description:      it.Description,
			StoreCode:        it.StoreCode,
			CNPJBase:         it.CNPJBase,
			StoreName:        it.StoreName,
			Unit:             it.Unit,
			Qty:              it.Qty.String(),
			UnitPrice:        it.UnitPrice.String(),
			TotalPrice:       it.TotalPrice.String(),
			SuggestedStem:    s.suggestStem(ctx, it.Description, it.Unit, brands),
			ResolvedBy:       it.ResolvedBy,
			ResolutionNote:   it.ResolutionNote,
			NeedsReview:      true,
			ProposalStatus:   proposalStatus(it.LastReviewAction),
			LastReviewAction: it.LastReviewAction,
			CreatedAt:        it.CreatedAt,
		}
		if it.PricePerUnit != nil {
			ppu := it.PricePerUnit.String()
			v.PricePerUnit = &ppu
		}
		if it.ProductID != nil {
			v.ProposedProduct = &ReviewProductView{
				ID:            *it.ProductID,
				ProductKey:    derefString(it.ProductKey),
				GTIN:          it.ProductGTIN,
				Name:          derefString(it.ProductName),
				Brand:         it.ProductBrand,
				Unit:          derefString(it.ProductUnit),
				CanonicalStem: it.ProductCanonicalStem,
			}
		}
		out = append(out, v)
	}
	return &ReviewQueuePage{Items: out, Page: newPage(page, limit, total)}, nil
}

// proposalStatus derives proposal state from the latest event. Missing and
// non-rejection actions (including assign and approve) describe a pending
// proposal; only the latest reject event produces rejected.
func proposalStatus(lastAction *string) string {
	if lastAction != nil && *lastAction == ActionReject {
		return ProposalStatusRejected
	}
	return ProposalStatusPending
}

// suggestStem derives the admin-facing canonical stem:
// 1. taxonomy canonical name when the produce table knows the stem
// 2. the reconstructed prefix expansion when an abbreviation applies, the raw normalized stem otherwise.
func (s *Service) suggestStem(ctx context.Context, description, unit string, brands *product.BrandDictionary) string {
	stem := product.NormalizeStem(description, unit, brands)
	if stem == "" {
		return ""
	}

	synStore := s.Stores.CommoditySynonyms()
	if syn, err := synStore.Canonicalize(ctx, stem); err == nil {
		// Standalone cheese aliases ("mofo", "azul") only make sense after a
		// cheese prefix, mirroring product.Classify.
		if syn.Source != product.CheeseAliasSource || syn.Synonym != stem {
			return syn.CanonicalName
		}
	}

	if pfx, err := synStore.MatchPrefix(ctx, stem); err == nil && pfx.PrefixExpansion != nil && strings.TrimSpace(*pfx.PrefixExpansion) != "" {
		expansion := strings.TrimSpace(*pfx.PrefixExpansion)
		remainder := strings.TrimSpace(stem[len(pfx.Synonym):])
		expanded := strings.TrimSpace(expansion + " " + remainder)
		changed := expanded != "" && expanded != stem

		if changed {
			if syn, err := synStore.Canonicalize(ctx, expanded); err == nil {
				return syn.CanonicalName
			}
		}

		if remSyn, err := product.ResolveCheeseAliasRemainder(ctx, synStore, pfx.Synonym, expansion, remainder); err == nil {
			return remSyn.CanonicalName
		}

		if changed {
			return expanded
		}
	}
	return stem
}

// SynonymRequest is the wire body of POST /admin/synonym: map a normalized
// receipt stem (synonym) onto a canonical product identity. PrefixExpansion
// registers an abbreviation (e.g. qj -> queijo) that the classifier expands
// before fuzzy matching.
type SynonymRequest struct {
	Synonym         string  `json:"synonym"`
	CanonicalName   string  `json:"canonical_name"`
	Category        string  `json:"category,omitempty"`
	Unit            string  `json:"unit,omitempty"`
	Source          string  `json:"source,omitempty"`
	PrefixExpansion *string `json:"prefix_expansion,omitempty"`
}

// SynonymView is the admin projection of a commodity_synonyms row.
type SynonymView struct {
	ID              int64     `json:"id"`
	Synonym         string    `json:"synonym"`
	CanonicalName   string    `json:"canonical_name"`
	Category        string    `json:"category,omitempty"`
	Unit            string    `json:"unit,omitempty"`
	Source          string    `json:"source,omitempty"`
	PrefixExpansion *string   `json:"prefix_expansion,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// CreateSynonym implements POST /admin/synonym. Admins map a normalized
// receipt stem to a canonical product identity; an optional prefix_expansion
// registers an abbreviation (qj -> queijo). Both the synonym and canonical
// name go through NormalizeStem so the taxonomy speaks the same vocabulary the
// parser produces. Duplicate synonyms are a conflict.
func (s *Service) CreateSynonym(ctx context.Context, req SynonymRequest) (*SynonymView, error) {
	synonym := product.NormalizeStem(req.Synonym, "", nil)
	canonical := product.NormalizeStem(req.CanonicalName, "", nil)
	if synonym == "" || canonical == "" {
		return nil, ErrInvalidAssignment
	}

	unit := ""
	if strings.TrimSpace(req.Unit) != "" {
		u, ok := product.NormalizeUnit(req.Unit)
		if !ok {
			return nil, ErrInvalidAssignment
		}
		unit = u
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "admin"
	}

	var prefix *string
	if req.PrefixExpansion != nil {
		p := product.NormalizeStem(*req.PrefixExpansion, "", nil)
		if p == "" {
			return nil, ErrInvalidAssignment
		}
		prefix = &p
	}

	row := &domain.CommoditySynonym{
		Synonym:         synonym,
		CanonicalName:   canonical,
		Category:        strings.TrimSpace(req.Category),
		Unit:            unit,
		Source:          source,
		PrefixExpansion: prefix,
	}
	if err := s.Stores.CommoditySynonyms().Create(ctx, row); err != nil {
		return nil, err
	}
	return toSynonymView(row), nil
}

func toSynonymView(row *domain.CommoditySynonym) *SynonymView {
	return &SynonymView{
		ID:              row.ID,
		Synonym:         row.Synonym,
		CanonicalName:   row.CanonicalName,
		Category:        row.Category,
		Unit:            row.Unit,
		Source:          row.Source,
		PrefixExpansion: row.PrefixExpansion,
		CreatedAt:       row.CreatedAt,
	}
}

// BatchAssign implements POST /admin/products/batch-assign atomically. Every
// assignment resolves to a product id (existing validated, create inserted via
// its commodity identity) and records it as the price_log's proposed product.
// The update keeps needs_review true: batch assignment no longer publishes a
// line, it only changes the pending proposal, which an explicit approve later
// publishes. Each successful update appends an "assign" event in the same
// transaction, so a failure leaves neither proposals nor events behind.
func (s *Service) BatchAssign(ctx context.Context, adminID int64, req BatchAssignRequest) (*BatchAssignResult, error) {
	if len(req.Assignments) == 0 {
		return nil, ErrInvalidAssignment
	}

	seen := map[int64]bool{}
	for i := range req.Assignments {
		a := &req.Assignments[i]
		if a.ID <= 0 || seen[a.ID] {
			return nil, ErrInvalidAssignment
		}
		seen[a.ID] = true
		switch a.Action {
		case "existing":
			if a.ProductID <= 0 {
				return nil, ErrInvalidAssignment
			}
		case "create":
			if strings.TrimSpace(a.Name) == "" {
				return nil, ErrInvalidAssignment
			}
			if _, ok := product.NormalizeUnit(a.Unit); !ok {
				return nil, ErrInvalidAssignment
			}
		default:
			return nil, ErrInvalidAssignment
		}
	}

	res := &BatchAssignResult{}
	err := s.Stores.Tx(ctx, func(tx domain.Stores) error {
		for i := range req.Assignments {
			assignment := &req.Assignments[i]

			// Load the current row before mutating so the assign event records
			// the previous product (nil on a first proposal) and a missing log
			// aborts the batch. The row lock serializes competing admin actions.
			log, err := tx.PriceLogs().GetByIDForUpdate(ctx, assignment.ID)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return ErrInvalidAssignment
				}
				return err
			}
			fromProductID := log.ProductID

			var productID int64
			switch assignment.Action {
			case "existing":
				// The target product must exist: assigning a dangling id would
				// silently poison future history reads. Use the write lookup so
				// an unpublished product (no approved price log yet) can still
				// be proposed.
				if _, err := tx.Products().GetByIDForWrite(ctx, assignment.ProductID); err != nil {
					if errors.Is(err, domain.ErrNotFound) {
						return ErrInvalidAssignment
					}
					return err
				}
				productID = assignment.ProductID
			case "create":
				p, created, err := s.createAdminProduct(ctx, tx, adminID, assignment.Name, assignment.Brand, assignment.Unit)

				if err != nil {
					return err
				}

				productID = p.ID
				if created {
					res.Created++
				}
			}

			if err := tx.PriceLogs().SetProductProposal(ctx, assignment.ID, productID, adminID, assignment.Action); err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return ErrInvalidAssignment
				}
				return err
			}

			toProductID := productID
			if err := tx.Events().Create(ctx, &domain.PriceLogEvent{
				PriceLogID:    assignment.ID,
				FromProductID: fromProductID,
				ToProductID:   &toProductID,
				UserID:        adminID,
				Action:        "assign",
			}); err != nil {
				return err
			}
			res.Assigned++
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// createAdminProduct inserts (or reuses) the product an admin named during a
// batch-assign. Admin products belong to commodity identity, so the key is the
// commodity hash of the normalized stem + canonical unit; two admins creating
// the same commodity converge on one row (idempotent, deduped).
func (s *Service) createAdminProduct(ctx context.Context, tx domain.Stores, adminID int64, name, brand, unit string) (*domain.Product, bool, error) {
	brands, err := brandDictionary(ctx, tx)
	if err != nil {
		return nil, false, err
	}
	name = strings.TrimSpace(name)
	brandName := strings.TrimSpace(brand)
	canonical, ok := product.NormalizeUnit(unit)
	if !ok {
		canonical = strings.TrimSpace(unit)
	}
	stem, key := product.CanonicalIdentity(name, brandName, canonical, brands)

	var brandPtr *string

	if brandName != "" {
		brandPtr = &brandName
	}

	if existing, err := tx.Products().GetByKey(ctx, key); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, false, err
	}

	stemCopy := stem
	createdBy := adminID

	p := &domain.Product{
		ProductKey:    key,
		Name:          name,
		Brand:         brandPtr,
		Unit:          canonical,
		CanonicalStem: &stemCopy,
		CreatedBy:     &createdBy,
	}
	created, err := tx.Products().Create(ctx, p)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			existing, getbykeyerr := tx.Products().GetByKey(ctx, key)
			if getbykeyerr != nil {
				return nil, false, getbykeyerr
			}
			return existing, false, nil
		}
		return nil, false, err
	}
	return p, created, nil
}

// MergeProducts implements POST /admin/products/merge: one transaction
// re-points every price_log of the duplicate product onto the canonical one
// and removes the duplicate row (FK keeps it whole). Unpublished products are
// valid endpoints on either side: the write lookup bypasses the public
// visibility guard, which only considers products with an approved price log.
func (s *Service) MergeProducts(ctx context.Context, fromID, toID int64) (*MergeResult, error) {
	if err := validateMerge(fromID, toID); err != nil {
		return nil, err
	}

	res := &MergeResult{FromID: fromID, ToID: toID}
	err := s.Stores.Tx(ctx, func(tx domain.Stores) error {
		if _, err := tx.Products().GetByIDForWrite(ctx, toID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrProductNotFound
			}
			return err
		}
		if _, err := tx.Products().GetByIDForWrite(ctx, fromID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrProductNotFound
			}
			return err
		}

		moved, err := tx.PriceLogs().RePointProduct(ctx, fromID, toID)
		if err != nil {
			return err
		}
		if err := tx.Products().Delete(ctx, fromID); err != nil {
			return err
		}
		res.LogsMoved = moved
		res.DupRemoved = true
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// MergeSupermarkets implements POST /admin/supermarkets/merge by CNPJ: the
// duplicate emitter's receipts move onto the canonical one and the duplicate
// row is removed, atomically.
func (s *Service) MergeSupermarkets(ctx context.Context, fromCNPJ, toCNPJ string) (*MergeSupermarketsResult, error) {
	fromCNPJ = strings.TrimSpace(fromCNPJ)
	toCNPJ = strings.TrimSpace(toCNPJ)
	if fromCNPJ == "" || toCNPJ == "" || fromCNPJ == toCNPJ {
		return nil, ErrInvalidAssignment
	}

	res := &MergeSupermarketsResult{}
	err := s.Stores.Tx(ctx, func(tx domain.Stores) error {
		from, err := tx.Supermarkets().GetByCNPJ(ctx, fromCNPJ)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrSupermarketNotFound
			}
			return err
		}
		to, err := tx.Supermarkets().GetByCNPJ(ctx, toCNPJ)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrSupermarketNotFound
			}
			return err
		}
		if from.ID == to.ID {
			return ErrInvalidAssignment
		}

		moved, err := tx.Supermarkets().RePointNfces(ctx, from.ID, to.ID)
		if err != nil {
			return err
		}
		if err := tx.Supermarkets().Delete(ctx, from.ID); err != nil {
			return err
		}
		res.FromID = from.ID
		res.ToID = to.ID
		res.NfcesMoved = moved
		res.DupRemoved = true
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// UpdateProduct implements PATCH /admin/products/{id}: curate name/brand/unit.
// The unit must normalize to the canonical vocabulary (else 400). Unpublished
// products are valid targets: curation is an admin write, not a public read.
//
// The patch and the derived identity commit together. A rename recomputes the
// canonical stem and product key; a brand/unit edit recomputes the key while
// keeping the stored stem. If the resulting key belongs to another product the
// request fails with ErrProductIdentityConflict and nothing is written.
func (s *Service) UpdateProduct(ctx context.Context, id int64, patch domain.ProductPatch) (*ProductView, error) {
	if id <= 0 {
		return nil, ErrProductNotFound
	}
	if patch.Unit != nil {
		u, ok := product.NormalizeUnit(*patch.Unit)
		if !ok {
			return nil, ErrInvalidFilter
		}
		patch.Unit = &u
	}

	var view ProductView
	// The public GetByID requires an approved price log, so it cannot be used
	// as an existence check for admin curation: it would report a product that
	// was just updated as missing. Resolve and re-read through the write lookup,
	// inside one transaction so a failed projection rolls the update back.
	err := s.Stores.Tx(ctx, func(tx domain.Stores) error {
		current, err := tx.Products().GetByIDForWrite(ctx, id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrProductNotFound
			}
			return err
		}

		// Creation and curation must derive identity the same way, including
		// the curated brand vocabulary and the empty-stem fallback.
		brands, err := brandDictionary(ctx, tx)
		if err != nil {
			return err
		}

		effectiveName := current.Name
		if patch.Name != nil {
			effectiveName = *patch.Name
		}
		effectiveBrand := ""
		if current.Brand != nil {
			effectiveBrand = *current.Brand
		}
		if patch.Brand != nil {
			effectiveBrand = *patch.Brand
		}
		effectiveUnit := current.Unit
		if patch.Unit != nil {
			effectiveUnit = *patch.Unit
		}
		if u, ok := product.NormalizeUnit(effectiveUnit); ok {
			effectiveUnit = u
		}

		storedStem := ""
		if current.CanonicalStem != nil {
			storedStem = *current.CanonicalStem
		}
		nameChanged := effectiveName != current.Name
		// A barcode row is identified by its GTIN; its display fields may be
		// curated without touching product_key. Recomputing it as a commodity
		// hash would let the next barcode receipt create a duplicate row.
		barcodeIdentity := product.IsBarcodeIdentity(current.ProductKey, current.GTIN)
		// The stem follows the name; the key follows name, brand and unit.
		// Recompute when any of them changes, and backfill a missing stem.
		identityChanged := !barcodeIdentity &&
			(nameChanged || patch.Brand != nil || patch.Unit != nil || storedStem == "")
		expectedStem := storedStem
		expectedKey := current.ProductKey
		if identityChanged {
			if nameChanged || storedStem == "" {
				expectedStem, expectedKey = product.CanonicalIdentity(effectiveName, effectiveBrand, effectiveUnit, brands)
			} else {
				expectedKey = product.CommodityKey(storedStem, effectiveUnit, effectiveBrand)
			}
		}

		// Resolve an existing owner before writing so the conflict message can
		// name the product and the field update never runs needlessly.
		if identityChanged && expectedKey != current.ProductKey {
			existing, err := tx.Products().GetByKey(ctx, expectedKey)
			switch {
			case err == nil && existing.ID != id:
				return fmt.Errorf("%w: key already belongs to product %d", ErrProductIdentityConflict, existing.ID)
			case err != nil && !errors.Is(err, domain.ErrNotFound):
				return err
			}
		}

		if err := tx.Products().Update(ctx, id, patch); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrProductNotFound
			}
			return err
		}
		if identityChanged && (expectedStem != storedStem || expectedKey != current.ProductKey) {
			if err := tx.Products().UpdateIdentity(ctx, id, expectedStem, expectedKey); err != nil {
				if errors.Is(err, domain.ErrConflict) {
					return ErrProductIdentityConflict
				}
				if errors.Is(err, domain.ErrNotFound) {
					return ErrProductNotFound
				}
				return err
			}
		}

		p, err := tx.Products().GetByIDForWrite(ctx, id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrProductNotFound
			}
			return err
		}
		view = toProductView(*p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &view, nil
}

// DeleteProduct implements DELETE /admin/products/{id} (?purge=true): refuses
// explicitly while price_logs still reference the product, unless purge was
// asked — then logs and product fall together in one transaction. Unpublished
// products are valid targets: the write lookup bypasses the public visibility
// guard, which only considers products with an approved price log.
func (s *Service) DeleteProduct(ctx context.Context, id int64, purge bool) (*DeleteProductResult, error) {
	if id <= 0 {
		return nil, ErrProductNotFound
	}

	count, err := s.Stores.PriceLogs().CountByProduct(ctx, id)
	if err != nil {
		return nil, err
	}
	res := &DeleteProductResult{ID: id, LogsDeleted: count}

	if count > 0 && !purge {
		return nil, ErrInUse
	}
	if _, err := s.Stores.Products().GetByIDForWrite(ctx, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	if count == 0 {
		if err := s.Stores.Products().Delete(ctx, id); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil, ErrProductNotFound
			}
			return nil, err
		}
		return res, nil
	}

	err = s.Stores.Tx(ctx, func(tx domain.Stores) error {
		if _, err := tx.PriceLogs().DeleteByProduct(ctx, id); err != nil {
			return err
		}
		if err := tx.Products().Delete(ctx, id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// DeleteSupermarket implements DELETE /api/v1/supermarkets/{id} (admin-only):
// refuses while the emitter still owns receipts, then removes the row.
func (s *Service) DeleteSupermarket(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrSupermarketNotFound
	}
	if _, err := s.Stores.Supermarkets().GetByID(ctx, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ErrSupermarketNotFound
		}
		return err
	}
	n, err := s.Stores.Supermarkets().CountNfces(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	if err := s.Stores.Supermarkets().Delete(ctx, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ErrSupermarketNotFound
		}
		return err
	}
	return nil
}

// ListUsers implements GET /admin/users.
func (s *Service) ListUsers(ctx context.Context, page, limit int) (*UserPage, error) {
	if err := validatePage(page, limit); err != nil {
		return nil, err
	}
	items, total, err := s.Stores.Users().List(ctx, limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	out := make([]UserView, 0, len(items))
	for i := range items {
		out = append(out, newUserView(&items[i]))
	}
	return &UserPage{Items: out, Page: newPage(page, limit, total)}, nil
}

// GetUser implements GET /admin/user/{id}: it returns the admin projection of
// one account, never the password hash.
func (s *Service) GetUser(ctx context.Context, id int64) (*UserView, error) {
	if id <= 0 {
		return nil, ErrUserNotFound
	}
	u, err := s.Stores.Users().GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	view := newUserView(u)
	return &view, nil
}

// newUserView projects a domain user onto the admin-safe response shape.
func newUserView(u *domain.User) UserView {
	return UserView{ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role, CreatedAt: u.CreatedAt}
}

// UpdateUserRole implements PATCH /admin/users/{id}/role. An admin may not
// change their own role: demoting yourself would strand the account, promoting
// adds nothing — both are guarded with 403.
func (s *Service) UpdateUserRole(ctx context.Context, callerID, userID int64, role string) error {
	role = strings.TrimSpace(role)
	if role != UserRoleUser && role != UserRoleAdmin {
		return ErrInvalidRole
	}
	if callerID == userID {
		return ErrForbidden
	}
	if _, err := s.Stores.Users().GetByID(ctx, userID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ErrUserNotFound
		}
		return err
	}
	return s.Stores.Users().UpdateRole(ctx, userID, role)
}

// User roles (mirrors the users.role values).
const (
	UserRoleUser  = "user"
	UserRoleAdmin = "admin"
)

func validateMerge(fromID, toID int64) error {
	if fromID <= 0 || toID <= 0 || fromID == toID {
		return ErrInvalidAssignment
	}
	return nil
}

// derefString returns the pointed-to string or "" for a nil pointer.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ReviewPriceLog handles admin decisions on manual assignments. An "approve"
// action publishes the current proposal (needs_review = false); a "reject"
// action keeps the proposal pending but records the decision in history. Both
// actions require a concrete proposed product, and an identical consecutive
// decision is refused until a new assign event resets the proposal state.
func (s *Service) ReviewPriceLog(ctx context.Context, adminID, priceLogID int64, action string) (*ReviewProductView, error) {
	if action != ActionApprove && action != ActionReject {
		return nil, ErrInvalidReviewAction
	}

	var result ReviewProductView
	err := s.Stores.Tx(ctx, func(tx domain.Stores) error {
		log, err := tx.PriceLogs().GetByIDForUpdate(ctx, priceLogID)
		if err != nil {
			return err
		}

		if !log.NeedsReview {
			return ErrPriceLogAlreadyProcessed
		}
		if latest, err := tx.Events().LatestByPriceLogID(ctx, log.ID); err == nil &&
			latest.Action == action {
			return ErrPriceLogAlreadyProcessed
		} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}

		if log.ProductID == nil || *log.ProductID <= 0 {
			if action == ActionApprove {
				return ErrMissingProductForApproval
			}
			return ErrMissingProductForRejection
		}

		// The proposal is still unpublished, so the public GetByID visibility
		// guard would hide it; the admin review needs the write-side read.
		product, err := tx.Products().GetByIDForWrite(ctx, *log.ProductID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrProductNotFound
			}
			return err
		}
		result = ReviewProductView{
			ID:            product.ID,
			ProductKey:    product.ProductKey,
			GTIN:          product.GTIN,
			Name:          product.Name,
			Brand:         product.Brand,
			Unit:          product.Unit,
			CanonicalStem: product.CanonicalStem,
		}

		if action == ActionApprove {
			if err := tx.PriceLogs().SetNeedsReview(ctx, log.ID, false); err != nil {
				return err
			}
		}

		return tx.Events().Create(ctx, &domain.PriceLogEvent{
			PriceLogID: log.ID, FromProductID: log.ProductID,
			ToProductID: log.ProductID, UserID: adminID, Action: action,
		})
	})

	if err != nil {
		return nil, fmt.Errorf("review price log: %w", err)
	}
	return &result, nil
}
