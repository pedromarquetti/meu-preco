package store

import (
	"context"

	"meupreco/internal/domain"
)

type userRepo struct{ db queryer }

func (r userRepo) Create(ctx context.Context, u *domain.User) error {
	err := r.db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role)
		 VALUES ($1, $2, COALESCE(NULLIF($3, ''), 'user'))
		 RETURNING id, created_at`,
		u.Email, u.PasswordHash, u.Role,
	).Scan(&u.ID, &u.CreatedAt)

	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return err
	}

	return nil
}

func (r userRepo) GetByID(ctx context.Context, id int64) (*domain.User, error) {
	u := &domain.User{}
	err := r.db.QueryRow(ctx,
		`SELECT id, email, password_hash, role, created_at FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)

	if err != nil {
		return nil, translateError(err)
	}

	return u, nil
}

func (r userRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	u := &domain.User{}
	err := r.db.QueryRow(ctx,
		`SELECT id, email, password_hash, role, created_at FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return u, nil
}

func (r userRepo) CountAdmin(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM users where role = 'admin'`).Scan(&n)
	return n, err
}

// List returns the users ordered by newest first, plus the total count for
// pagination (email/password_hash kept: an admin listing may render a profile).
func (r userRepo) List(ctx context.Context, limit, offset int) ([]domain.User, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.Query(ctx,
		`SELECT id, email, password_hash, role, created_at FROM users
		 ORDER BY created_at DESC, id
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]domain.User, 0, limit)
	for rows.Next() {
		u := domain.User{}
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// UpdateRole changes a user's role; the caller (service) validates the value.
func (r userRepo) UpdateRole(ctx context.Context, id int64, role string) error {
	tag, err := r.db.Exec(ctx, `UPDATE users SET role = $2 WHERE id = $1`, id, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
