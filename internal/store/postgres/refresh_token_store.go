package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"meupreco/internal/domain"
)

type refreshTokenRepo struct{ db queryer }

func (r refreshTokenRepo) Create(ctx context.Context, t *domain.RefreshToken) error {
	err := r.db.QueryRow(ctx,
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
//
// The repo is pool-backed in the auth flow, where the three statements run
// inside their own transaction; when it is bound to a txStores the enclosing
// caller transaction already provides atomicity and the statements run in
// place (no nested BEGIN).
func (r refreshTokenRepo) Rotate(ctx context.Context, oldHash string, next *domain.RefreshToken) error {
	if tx, ok := r.db.(pgx.Tx); ok {
		return r.rotate(ctx, tx, oldHash, next)
	}

	pool, ok := r.db.(*pgxpool.Pool)
	if !ok {
		return errNestedTx
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		return r.rotate(ctx, tx, oldHash, next)
	})
}

// rotate runs the revoke-then-insert sequence on the given tx.
func (r refreshTokenRepo) rotate(ctx context.Context, tx pgx.Tx, oldHash string, next *domain.RefreshToken) error {
	var revoked bool
	err := tx.QueryRow(ctx,
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

	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = true WHERE token_hash = $1`, oldHash); err != nil {
		return err
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4)`,
		next.UserID, next.FamilyID, next.TokenHash, next.ExpiresAt)
	return err
}

func (r refreshTokenRepo) RevokeFamily(ctx context.Context, familyID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = true WHERE family_id = $1 AND revoked = false`, familyID)
	return err
}

// errNestedTx reports a Tx attempt inside a transaction (not supported; the
// outer transaction is the single atomic unit).
var errNestedTx = &pgconn.PgError{Message: "nested transactions are not supported"}
