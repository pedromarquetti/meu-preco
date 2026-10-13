package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

type nfceRepo struct{ db queryer }

func (r nfceRepo) Create(ctx context.Context, n *domain.Nfce) error {
	err := r.db.QueryRow(ctx,
		`INSERT INTO nfces (access_key, supermarket_id, number, series, issued_at, protocol,
		                   environment, total, discount, paid_value, payment_method, coo,
		                   payload_raw, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11, $12, $13, $14)
		 ON CONFLICT (access_key) DO NOTHING
		 RETURNING id, created_at`,
		n.AccessKey, n.Supermarket.ID, n.Number, n.Series, n.IssuedAt, n.Protocol,
		n.Environment, n.Total.String(), n.Discount.String(), n.PaidValue.String(),
		n.PaymentMethod, n.COO, n.PayloadRaw, n.CreatedBy,
	).Scan(&n.ID, &n.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already ingested: DO NOTHING returns no row instead of raising a
			// unique violation, which would abort a live transaction.
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

func (r nfceRepo) GetByID(ctx context.Context, id int64) (*domain.Nfce, error) {
	n := &domain.Nfce{}
	err := r.scanOne(ctx,
		`WHERE n.id = $1`, id,
		n)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func (r nfceRepo) GetByAccessKey(ctx context.Context, accessKey string) (*domain.Nfce, error) {
	n := &domain.Nfce{AccessKey: accessKey}
	err := r.scanOne(ctx,
		`WHERE n.access_key = $1`, accessKey,
		n)
	if err != nil {
		return nil, err
	}
	return n, nil
}

// Delete removes a receipt row, or ErrNotFound. Callers must clear its
// price_logs and preview drafts first (service runs the unit in one Tx).
func (r nfceRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM nfces WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// scanOne runs the shared Nfce select (with supermarket joined) for a single
// row and decodes numbers as text→decimal.
func (r nfceRepo) scanOne(ctx context.Context, where string, arg any, n *domain.Nfce) error {
	var total, discount, paid string

	err := r.db.QueryRow(ctx,
		`SELECT n.id, n.access_key, n.number, n.series, n.issued_at, n.protocol,
		        n.environment, n.total::text, n.discount::text, n.paid_value::text,
		        n.payment_method, n.coo, n.payload_raw, n.created_by, n.created_at,
		        s.id, s.cnpj, s.legal_name, s.address
		 FROM nfces n JOIN supermarkets s ON s.id = n.supermarket_id
		 `+where, arg,
	).Scan(&n.ID, &n.AccessKey, &n.Number, &n.Series, &n.IssuedAt, &n.Protocol,
		&n.Environment, &total, &discount, &paid, &n.PaymentMethod,
		&n.COO, &n.PayloadRaw, &n.CreatedBy, &n.CreatedAt,
		&n.Supermarket.ID, &n.Supermarket.CNPJ, &n.Supermarket.LegalName, &n.Supermarket.Address,
	)
	if err != nil {
		return translateError(err)
	}

	dec := func(s string) decimal.Decimal {
		return decimal.RequireFromString(s)
	}
	n.Total = dec(total)
	n.Discount = dec(discount)
	n.PaidValue = dec(paid)
	return nil
}
