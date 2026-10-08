package store

import (
	"context"

	"meupreco/internal/domain"
)

type userRepo struct{ s *Store }

func (r userRepo) Create(ctx context.Context, u *domain.User) error {
	err := r.s.pool.QueryRow(ctx,
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
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, role, created_at FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)

	if err != nil {
		return nil, translateError(err)
	}

	return u, nil
}

func (r userRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	u := &domain.User{}
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, role, created_at FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return u, nil
}
