package store

import (
	"context"

	"meupreco/internal/domain"
)

type previewRepo struct{ s *Store }

func (r previewRepo) Create(ctx context.Context, p *domain.Preview) error {
	err := r.s.pool.QueryRow(ctx,
		`INSERT INTO previews (user_id, access_key, plan_items, plan_totals, status, idempotency_key, ttl)
		 VALUES ($1, $2, $3::jsonb, $4::jsonb, $5, $6, $7)
		 RETURNING id, created_at`,
		p.UserID, p.AccessKey, p.PlanItems, p.PlanTotals, p.Status, p.IdempotencyKey, p.TTL,
	).Scan(&p.ID, &p.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

func (r previewRepo) GetByID(ctx context.Context, id int64) (*domain.Preview, error) {
	p := &domain.Preview{}
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, user_id, access_key, plan_items, plan_totals, status, idempotency_key, ttl, created_at
		 FROM previews WHERE id = $1`, id,
	).Scan(&p.ID, &p.UserID, &p.AccessKey, &p.PlanItems, &p.PlanTotals, &p.Status,
		&p.IdempotencyKey, &p.TTL, &p.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return p, nil
}

func (r previewRepo) GetByIdempotencyKey(ctx context.Context, idempotencyKey string) (*domain.Preview, error) {
	p := &domain.Preview{}
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, user_id, access_key, plan_items, plan_totals, status, idempotency_key, ttl, created_at
		 FROM previews WHERE idempotency_key = $1`, idempotencyKey,
	).Scan(&p.ID, &p.UserID, &p.AccessKey, &p.PlanItems, &p.PlanTotals, &p.Status,
		&p.IdempotencyKey, &p.TTL, &p.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return p, nil
}

// MarkCommitted flips a pending preview to committed. It returns ErrConflict if
// the preview was already committed (double confirm) and ErrNotFound if it no
// longer exists.
func (r previewRepo) MarkCommitted(ctx context.Context, id int64) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE previews SET status = 'committed' WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	p, err := r.GetByID(ctx, id)
	if err != nil {
		return translateError(err)
	}
	if p.Status == "committed" {
		return domain.ErrConflict
	}
	return domain.ErrConflict
}
