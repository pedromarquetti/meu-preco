package store

import (
	"context"
	"strings"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

type priceLogRepo struct{ s *Store }

func (r priceLogRepo) Create(ctx context.Context, l *domain.PriceLog) error {
	err := r.s.pool.QueryRow(ctx,
		`INSERT INTO price_logs (nfce_id, product_id, line, description_snapshot,
		                        qty, unit, normalized_unit, price_per_unit,
		                        unit_price, total_price, identity_status,
		                        needs_review, resolved_by, resolution_note)
		 VALUES ($1, $2, $3, $4, $5::numeric, $6, $7, $8::numeric, $9::numeric, $10::numeric,
		         $11, $12, $13, $14)
		 RETURNING id, created_at`,
		l.NfceID, l.ProductID, l.Line, l.DescriptionSnapshot,
		l.Qty.String(), l.Unit, l.NormalizedUnit, scanDecimalPtrOrNil(l.PricePerUnit),
		l.UnitPrice.String(), l.TotalPrice.String(), l.IdentityStatus,
		l.NeedsReview, l.ResolvedBy, l.ResolutionNote,
	).Scan(&l.ID, &l.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
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

	err := r.s.pool.QueryRow(ctx,
		`SELECT id, nfce_id, product_id, line, description_snapshot,
		        qty::text, unit, normalized_unit, price_per_unit::text,
		        unit_price::text, total_price::text, identity_status,
		        needs_review, resolved_by, resolution_note, created_at
		 FROM price_logs `+where, args...,
	).Scan(&l.ID, &l.NfceID, &l.ProductID, &l.Line, &l.DescriptionSnapshot,
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
