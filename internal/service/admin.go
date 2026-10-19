package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

// Admin-only curation: the needs-review queue, batch assigning to
// existing or brand-new products, product/supermarket merges and deletes, and
// user role management. Every mutation lives behind a single Stores.Tx so a
// failed batch never leaves a half-applied plan.

// ReviewItemView is one queue row: the raw line plus the store context an admin
// needs to curate it, with a suggested canonical stem for the create path.
type ReviewItemView struct {
	PriceLogID    int64     `json:"price_log_id"`
	NfceID        int64     `json:"nfce_id"`
	AccessKey     string    `json:"access_key"`
	Line          int       `json:"line"`
	Description   string    `json:"description"`
	StoreCode     string    `json:"store_code"`
	CNPJBase      string    `json:"cnpj_base"`
	StoreName     string    `json:"store_name"`
	Unit          string    `json:"unit"`
	Qty           string    `json:"qty"`
	PricePerUnit  *string   `json:"price_per_unit,omitempty"`
	UnitPrice     string    `json:"unit_price"`
	TotalPrice    string    `json:"total_price"`
	SuggestedStem string    `json:"suggested_stem"`
	CreatedAt     time.Time `json:"created_at"`
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

// BatchAssignResult reports the atomic outcome of a batch.
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
	for _, it := range items {
		v := ReviewItemView{
			PriceLogID:    it.PriceLogID,
			NfceID:        it.NfceID,
			AccessKey:     it.AccessKey,
			Line:          it.Line,
			Description:   it.Description,
			StoreCode:     it.StoreCode,
			CNPJBase:      it.CNPJBase,
			StoreName:     it.StoreName,
			Unit:          it.Unit,
			Qty:           it.Qty.String(),
			UnitPrice:     it.UnitPrice.String(),
			TotalPrice:    it.TotalPrice.String(),
			SuggestedStem: s.suggestStem(ctx, it.Description, it.Unit),
			CreatedAt:     it.CreatedAt,
		}
		if it.PricePerUnit != nil {
			ppu := it.PricePerUnit.String()
			v.PricePerUnit = &ppu
		}
		out = append(out, v)
	}
	return &ReviewQueuePage{Items: out, Page: newPage(page, limit, total)}, nil
}

// suggestStem derives the admin-facing canonical stem: taxonomy canonical name
// when the produce table knows the stem, the raw normalized stem otherwise.
func (s *Service) suggestStem(ctx context.Context, description, unit string) string {
	stem := product.NormalizeStem(description, unit, nil)
	if stem == "" {
		return ""
	}
	if syn, err := s.Stores.CommoditySynonyms().Canonicalize(ctx, stem); err == nil {
		return syn.CanonicalName
	}
	return stem
}

// BatchAssign implements POST /admin/products/batch-assign atomically: every
// assignment resolves to a product id (existing validated, create inserted via
// its commodity identity), then re-points the price_log and clears its
// needs_review flag. One bad entry aborts the whole unit.
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
			a := &req.Assignments[i]

			var productID int64
			switch a.Action {
			case "existing":
				// The target product must exist: assigning a dangling id would
				// silently poison future history reads.
				if _, err := tx.Products().GetByID(ctx, a.ProductID); err != nil {
					if errors.Is(err, domain.ErrNotFound) {
						return ErrInvalidAssignment
					}
					return err
				}
				productID = a.ProductID
			case "create":
				p, err := s.createAdminProduct(ctx, tx, adminID, a.Name, a.Brand, a.Unit)
				if err != nil {
					return err
				}
				productID = p.ID
				res.Created++
			}

			if err := tx.PriceLogs().SetProduct(ctx, a.ID, productID, adminID, a.Action); err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					return ErrInvalidAssignment
				}
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
func (s *Service) createAdminProduct(ctx context.Context, tx domain.Stores, adminID int64, name, brand, unit string) (*domain.Product, error) {
	canonical, _ := product.NormalizeUnit(unit)
	name = strings.TrimSpace(name)

	stem := product.NormalizeStem(name, unit, nil)
	if stem == "" {
		// Nothing identifiable remains (all-numeric/packaging-only names): fall
		// back to a stable hash of the exact triple so the creation is still
		// deterministic and idempotent.
		stem = stableKey(name, strings.TrimSpace(brand), canonical)
	}

	var brandPtr *string
	if b := strings.TrimSpace(brand); b != "" {
		brandPtr = &b
	}
	stemCopy := stem
	createdBy := adminID

	p := &domain.Product{
		ProductKey:    product.CommodityKey(stem, canonical),
		Name:          name,
		Brand:         brandPtr,
		Unit:          canonical,
		CanonicalStem: &stemCopy,
		CreatedBy:     &createdBy,
	}
	if err := tx.Products().Create(ctx, p); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return tx.Products().GetByKey(ctx, p.ProductKey)
		}
		return nil, err
	}
	return p, nil
}

// MergeProducts implements POST /admin/products/merge: one transaction
// re-points every price_log of the duplicate product onto the canonical one
// and removes the duplicate row (FK keeps it whole).
func (s *Service) MergeProducts(ctx context.Context, fromID, toID int64) (*MergeResult, error) {
	if err := validateMerge(fromID, toID); err != nil {
		return nil, err
	}

	res := &MergeResult{FromID: fromID, ToID: toID}
	err := s.Stores.Tx(ctx, func(tx domain.Stores) error {
		if _, err := tx.Products().GetByID(ctx, toID); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return ErrProductNotFound
			}
			return err
		}
		if _, err := tx.Products().GetByID(ctx, fromID); err != nil {
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
// The unit must normalize to the canonical vocabulary (else 400).
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
	if err := s.Stores.Products().Update(ctx, id, patch); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	p, err := s.Stores.Products().GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	v := toProductView(*p)
	return &v, nil
}

// DeleteProduct implements DELETE /admin/products/{id} (?purge=true): refuses
// explicitly while price_logs still reference the product, unless purge was
// asked — then logs and product fall together in one transaction.
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
	if _, err := s.Stores.Products().GetByID(ctx, id); err != nil {
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
	for _, u := range items {
		out = append(out, UserView{ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role, CreatedAt: u.CreatedAt})
	}
	return &UserPage{Items: out, Page: newPage(page, limit, total)}, nil
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

func stableKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}
