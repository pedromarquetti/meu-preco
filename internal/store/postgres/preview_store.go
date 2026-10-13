package store

import (
	"context"

	"meupreco/internal/domain"
)

type previewRepo struct{ db queryer }

func (r previewRepo) Create(ctx context.Context, p *domain.Preview) error {
	err := r.db.QueryRow(ctx,
		`INSERT INTO previews (user_id, access_key, plan_items, plan_totals, status, ttl)
		 VALUES ($1, $2, $3::jsonb, $4::jsonb, $5, $6)
		 RETURNING id, created_at`,
		p.UserID, p.AccessKey, p.PlanItems, p.PlanTotals, p.Status, p.TTL,
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
	err := r.db.QueryRow(ctx,
		`SELECT id, user_id, access_key, plan_items, plan_totals, status, ttl, created_at
		 FROM previews WHERE id = $1`, id,
	).Scan(&p.ID, &p.UserID, &p.AccessKey, &p.PlanItems, &p.PlanTotals, &p.Status,
		&p.TTL, &p.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return p, nil
}

func (r previewRepo) GetPendingByUserAccessKey(ctx context.Context, userID int64, accessKey string) (*domain.Preview, error) {
	p := &domain.Preview{}
	err := r.db.QueryRow(ctx,
		`SELECT id, user_id, access_key, plan_items, plan_totals, status, ttl, created_at
		 FROM previews WHERE user_id = $1 AND access_key = $2 AND status = 'pending'`, userID, accessKey,
	).Scan(&p.ID, &p.UserID, &p.AccessKey, &p.PlanItems, &p.PlanTotals, &p.Status,
		&p.TTL, &p.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return p, nil
}

// MarkCommitted flips a pending preview to committed. It returns ErrConflict if
// the preview was already committed (double confirm) and ErrNotFound if it no
// longer exists.
func (r previewRepo) MarkCommitted(ctx context.Context, id int64) error {
	tag, err := r.db.Exec(ctx,
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

// PurgeExpired deletes preview rows past their ttl regardless of status; the
// write paths (preview/commit) call it as a sweep so the table cannot grow
// unbounded with stale plans.
func (r previewRepo) PurgeExpired(ctx context.Context) (int64, error) {
	tag, err := r.db.Exec(ctx,
		`DELETE FROM previews WHERE ttl < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteByAccessKey removes every pending/committed draft of a receipt across
// all users (the receipt itself is being deleted); returns the count removed.
func (r previewRepo) DeleteByAccessKey(ctx context.Context, accessKey string) (int64, error) {
	tag, err := r.db.Exec(ctx, `DELETE FROM previews WHERE access_key = $1`, accessKey)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
