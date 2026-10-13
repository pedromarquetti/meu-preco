package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

type priceLogRepo struct{ db queryer }

func (r priceLogRepo) Create(ctx context.Context, l *domain.PriceLog) error {
	err := r.db.QueryRow(ctx,
		`INSERT INTO price_logs (nfce_id, product_id, line, description_snapshot, store_code,
		                        qty, unit, normalized_unit, price_per_unit,
		                        unit_price, total_price, identity_status,
		                        needs_review, resolved_by, resolution_note)
		 VALUES ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9::numeric, $10::numeric, $11::numeric,
		         $12, $13, $14, $15)
		 ON CONFLICT (nfce_id, line) DO NOTHING
		 RETURNING id, created_at`,
		l.NfceID, l.ProductID, l.Line, l.DescriptionSnapshot, l.StoreCode,
		l.Qty.String(), l.Unit, l.NormalizedUnit, scanDecimalPtrOrNil(l.PricePerUnit),
		l.UnitPrice.String(), l.TotalPrice.String(), l.IdentityStatus,
		l.NeedsReview, l.ResolvedBy, l.ResolutionNote,
	).Scan(&l.ID, &l.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// (nfce_id, line) already logged: DO NOTHING returns no row
			// instead of a unique violation, keeping a live commit tx healthy.
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

func (r priceLogRepo) GetByID(ctx context.Context, id int64) (*domain.PriceLog, error) {
	return r.scanOne(ctx, `WHERE id = $1`, id)
}

func (r priceLogRepo) GetByNfceLine(ctx context.Context, nfceID int64, line int) (*domain.PriceLog, error) {
	return r.scanOne(ctx, `WHERE nfce_id = $1 AND line = $2`, nfceID, line)
}

// scanOne decodes a price_log row; decimal NUMERICs are fetched as text to
// avoid a float round-trip.
func (r priceLogRepo) scanOne(ctx context.Context, where string, args ...any) (*domain.PriceLog, error) {
	l := &domain.PriceLog{}
	var (
		qty, unitPrice, totalPrice string
		pricePerUnit               *string
	)

	err := r.db.QueryRow(ctx,
		`SELECT id, nfce_id, product_id, line, description_snapshot, store_code,
		        qty::text, unit, normalized_unit, price_per_unit::text,
		        unit_price::text, total_price::text, identity_status,
		        needs_review, resolved_by, resolution_note, created_at
		 FROM price_logs `+where, args...,
	).Scan(&l.ID, &l.NfceID, &l.ProductID, &l.Line, &l.DescriptionSnapshot, &l.StoreCode,
		&qty, &l.Unit, &l.NormalizedUnit, &pricePerUnit,
		&unitPrice, &totalPrice, &l.IdentityStatus,
		&l.NeedsReview, &l.ResolvedBy, &l.ResolutionNote, &l.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}

	dec := func(s string) decimal.Decimal { return decimal.RequireFromString(s) }
	l.Qty = dec(qty)
	l.UnitPrice = dec(unitPrice)
	l.TotalPrice = dec(totalPrice)
	if pricePerUnit != nil {
		p := dec(*pricePerUnit)
		l.PricePerUnit = &p
	}
	return l, nil
}

// scanDecimalPtrOrNil renders a *decimal.Decimal for a ::numeric parameter.
func scanDecimalPtrOrNil(d *decimal.Decimal) any {
	if d == nil {
		return nil
	}
	return strings.TrimSpace(d.String())
}

// HistoryByProduct returns the price curve for a product, each point annotated
// with its receipt and emitter, optionally windowed by issued_at.
func (r priceLogRepo) HistoryByProduct(ctx context.Context, productID int64, from, to *time.Time) ([]domain.PricePoint, error) {
	where := `l.product_id = $1`
	args := []any{productID}
	if from != nil {
		args = append(args, *from)
		where += fmt.Sprintf(" AND n.issued_at >= $%d", len(args))
	}
	if to != nil {
		args = append(args, *to)
		where += fmt.Sprintf(" AND n.issued_at <= $%d", len(args))
	}

	rows, err := r.db.Query(ctx,
		`SELECT l.id, l.nfce_id, n.access_key, s.id, s.cnpj, s.legal_name, l.line,
		        l.qty::text, l.unit, l.normalized_unit, l.price_per_unit::text,
		        l.unit_price::text, l.total_price::text, n.issued_at, l.created_at
		 FROM price_logs l
		 JOIN nfces n       ON n.id = l.nfce_id
		 JOIN supermarkets s ON s.id = n.supermarket_id
		 WHERE `+where+`
		 ORDER BY n.issued_at ASC NULLS LAST, l.created_at, l.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.PricePoint, 0, 64)
	for rows.Next() {
		p, err := scanPricePoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// scanPricePoint decodes the HistoryByProduct column shape.
func scanPricePoint(rows pgx.Rows) (*domain.PricePoint, error) {
	p := &domain.PricePoint{}
	var qty, unitPrice, totalPrice string
	var ppu *string
	err := rows.Scan(&p.PriceLogID, &p.NfceID, &p.AccessKey, &p.SupermarketID, &p.StoreCNPJ, &p.StoreName,
		&p.Line, &qty, &p.Unit, &p.NormalizedUnit, &ppu,
		&unitPrice, &totalPrice, &p.IssuedAt, &p.LoggedAt)
	if err != nil {
		return nil, err
	}
	p.Qty = decimal.RequireFromString(qty)
	p.UnitPrice = decimal.RequireFromString(unitPrice)
	p.TotalPrice = decimal.RequireFromString(totalPrice)
	if ppu != nil {
		v := decimal.RequireFromString(*ppu)
		p.PricePerUnit = &v
	}
	return p, nil
}

// LatestBySupermarket reads the products sold at a supermarket with their most
// recent logged price, never surfacing review rows, plus the distinct-product
// total for pagination.
func (r priceLogRepo) LatestBySupermarket(ctx context.Context, supermarketID int64, limit, offset int) ([]domain.SupermarketProduct, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	const base = `FROM price_logs l
		JOIN nfces n     ON n.id = l.nfce_id
		JOIN products p  ON p.id = l.product_id
		WHERE n.supermarket_id = $1 AND l.product_id IS NOT NULL AND p.needs_review = false`

	var total int64
	if err := r.db.QueryRow(ctx, `SELECT count(DISTINCT p.id) `+base, supermarketID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.Query(ctx,
		`SELECT DISTINCT ON (p.id)
		        p.id, p.product_key, p.gtin, p.name, p.brand, p.unit, p.canonical_stem, p.needs_review, p.created_by, p.created_at,
		        l.id, l.qty::text, l.normalized_unit, l.price_per_unit::text,
		        l.unit_price::text, l.total_price::text, n.issued_at, l.created_at
		 `+base+`
		 ORDER BY p.id, n.issued_at DESC NULLS LAST, l.created_at DESC, l.id DESC
		 LIMIT $2 OFFSET $3`, supermarketID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]domain.SupermarketProduct, 0, limit)
	for rows.Next() {
		sp := domain.SupermarketProduct{}
		var qty, unitPrice, totalPrice string
		var ppu *string
		if err := rows.Scan(&sp.Product.ID, &sp.Product.ProductKey, &sp.Product.GTIN, &sp.Product.Name,
			&sp.Product.Brand, &sp.Product.Unit, &sp.Product.CanonicalStem, &sp.Product.NeedsReview,
			&sp.Product.CreatedBy, &sp.Product.CreatedAt,
			&sp.PriceLogID, &qty, &sp.NormalizedUnit, &ppu,
			&unitPrice, &totalPrice, &sp.LastIssuedAt, &sp.LastLoggedAt); err != nil {
			return nil, 0, err
		}
		sp.Qty = decimal.RequireFromString(qty)
		sp.UnitPrice = decimal.RequireFromString(unitPrice)
		sp.TotalPrice = decimal.RequireFromString(totalPrice)
		if ppu != nil {
			v := decimal.RequireFromString(*ppu)
			sp.PricePerUnit = &v
		}
		out = append(out, sp)
	}
	return out, total, rows.Err()
}

// NeedsReview reads the admin review queue (price_log rows flagged
// needs_review) annotated with store code, CNPJ base and store name, plus the
// queue size for pagination.
func (r priceLogRepo) NeedsReview(ctx context.Context, limit, offset int) ([]domain.ReviewItem, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM price_logs WHERE needs_review = true`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.Query(ctx,
		`SELECT l.id, l.nfce_id, n.access_key, l.line, l.description_snapshot,
		        COALESCE(l.store_code, ''), left(s.cnpj, 8), s.legal_name,
		        l.unit, l.qty::text, l.price_per_unit::text,
		        l.unit_price::text, l.total_price::text, l.created_at
		 FROM price_logs l
		 JOIN nfces n       ON n.id = l.nfce_id
		 JOIN supermarkets s ON s.id = n.supermarket_id
		 WHERE l.needs_review = true
		 ORDER BY l.created_at, l.id
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]domain.ReviewItem, 0, limit)
	for rows.Next() {
		it := domain.ReviewItem{}
		var qty, unitPrice, totalPrice string
		var ppu *string
		if err := rows.Scan(&it.PriceLogID, &it.NfceID, &it.AccessKey, &it.Line, &it.Description,
			&it.StoreCode, &it.CNPJBase, &it.StoreName,
			&it.Unit, &qty, &ppu, &unitPrice, &totalPrice, &it.CreatedAt); err != nil {
			return nil, 0, err
		}
		it.Qty = decimal.RequireFromString(qty)
		it.UnitPrice = decimal.RequireFromString(unitPrice)
		it.TotalPrice = decimal.RequireFromString(totalPrice)
		if ppu != nil {
			v := decimal.RequireFromString(*ppu)
			it.PricePerUnit = &v
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// RePointProduct re-attaches every log to another product (admin merge);
// returns how many logs moved.
func (r priceLogRepo) RePointProduct(ctx context.Context, fromID, toID int64) (int64, error) {
	if fromID == toID {
		return 0, nil
	}
	tag, err := r.db.Exec(ctx, `UPDATE price_logs SET product_id = $2 WHERE product_id = $1`, fromID, toID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// SetProduct re-points one log to a product and clears its needs_review flag
// (admin batch-assign).
func (r priceLogRepo) SetProduct(ctx context.Context, priceLogID, productID int64, resolvedBy int64, note string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE price_logs SET product_id = $2, needs_review = false, resolved_by = $3, resolution_note = $4
		 WHERE id = $1`, priceLogID, productID, resolvedBy, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DeleteByNfceID removes every log of a receipt; returns the removed count.
func (r priceLogRepo) DeleteByNfceID(ctx context.Context, nfceID int64) (int64, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM price_logs WHERE nfce_id = $1`, nfceID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteByProduct removes every log referencing a product; returns the count.
func (r priceLogRepo) DeleteByProduct(ctx context.Context, productID int64) (int64, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM price_logs WHERE product_id = $1`, productID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CountByProduct reports how many logs still reference a product.
func (r priceLogRepo) CountByProduct(ctx context.Context, productID int64) (int64, error) {
	var n int64
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM price_logs WHERE product_id = $1`, productID).Scan(&n)
	return n, err
}
