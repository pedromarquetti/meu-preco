package store

import (
	"context"

	"meupreco/internal/domain"
)

type supermarketRepo struct{ s *Store }

func (r supermarketRepo) Upsert(ctx context.Context, s *domain.Supermarket) error {
	err := r.s.pool.QueryRow(ctx,
		`INSERT INTO supermarkets (cnpj, legal_name, address)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (cnpj) DO UPDATE
		 SET legal_name = EXCLUDED.legal_name,
		     address    = EXCLUDED.address
		 RETURNING id, created_at`,
		s.CNPJ, s.LegalName, s.Address,
	).Scan(&s.ID, &s.CreatedAt)
	return err
}

func (r supermarketRepo) GetByID(ctx context.Context, id int64) (*domain.Supermarket, error) {
	s := &domain.Supermarket{ID: id}
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, cnpj, legal_name, address, created_at FROM supermarkets WHERE id = $1`, id,
	).Scan(&s.ID, &s.CNPJ, &s.LegalName, &s.Address, &s.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return s, nil
}

func (r supermarketRepo) GetByCNPJ(ctx context.Context, cnpj string) (*domain.Supermarket, error) {
	s := &domain.Supermarket{}
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, cnpj, legal_name, address, created_at FROM supermarkets WHERE cnpj = $1`, cnpj,
	).Scan(&s.ID, &s.CNPJ, &s.LegalName, &s.Address, &s.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return s, nil
}
