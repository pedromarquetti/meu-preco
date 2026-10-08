package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"meupreco/internal/domain"
)

type refreshTokenRepo struct{ s *Store }

func (r refreshTokenRepo) Create(ctx context.Context, t *domain.RefreshToken) error {
	err := r.s.pool.QueryRow(ctx,
		`INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, revoked, created_at`,
		t.UserID, t.FamilyID, t.TokenHash, t.ExpiresAt,
	).Scan(&t.ID, &t.Revoked, &t.CreatedAt)

	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

// Rotate atomically consumes oldHash and inserts its successor. The row is
// locked FOR UPDATE so two concurrent refreshes of the same token cannot
// both succeed; the loser sees the revoked row (or its absence) and fails.
func (r refreshTokenRepo) Rotate(ctx context.Context, oldHash string, next *domain.RefreshToken) error {
	_, err := r.s.pool.Exec(ctx, `BEGIN`)
	if err != nil {
		return err
	}
	defer r.s.pool.Exec(ctx, `ROLLBACK`)

	var revoked bool
	err = r.s.pool.QueryRow(ctx,
		`SELECT revoked FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE`, oldHash,
	).Scan(&revoked)

	switch {
	case err == pgx.ErrNoRows:
		return domain.ErrNotFound
	case err != nil:
		return err
	case revoked:
		return domain.ErrConflict
	}

	_, err = r.s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = true WHERE token_hash = $1`, oldHash)
	if err != nil {
		return err
	}

	_, err = r.s.pool.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4)`,
		next.UserID, next.FamilyID, next.TokenHash, next.ExpiresAt)
	if err != nil {
		return err
	}

	_, err = r.s.pool.Exec(ctx, `COMMIT`)
	return err
}

func (r refreshTokenRepo) RevokeFamily(ctx context.Context, familyID string) error {
	_, err := r.s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = true WHERE family_id = $1 AND revoked = false`, familyID)
	return err
}
