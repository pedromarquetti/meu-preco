package store

import (
	"context"

	"meupreco/internal/domain"
)

type supermarketRepo struct{ db queryer }

func (r supermarketRepo) Upsert(ctx context.Context, s *domain.Supermarket) error {
	err := r.db.QueryRow(ctx,
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
	err := r.db.QueryRow(ctx,
		`SELECT id, cnpj, legal_name, address, created_at FROM supermarkets WHERE id = $1`, id,
	).Scan(&s.ID, &s.CNPJ, &s.LegalName, &s.Address, &s.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return s, nil
}

func (r supermarketRepo) GetByCNPJ(ctx context.Context, cnpj string) (*domain.Supermarket, error) {
	s := &domain.Supermarket{}
	err := r.db.QueryRow(ctx,
		`SELECT id, cnpj, legal_name, address, created_at FROM supermarkets WHERE cnpj = $1`, cnpj,
	).Scan(&s.ID, &s.CNPJ, &s.LegalName, &s.Address, &s.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return s, nil
}

// List returns the known supermarkets ordered by legal_name, plus the total
// count for pagination.
func (r supermarketRepo) List(ctx context.Context, limit, offset int) ([]domain.Supermarket, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM supermarkets`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.db.Query(ctx,
		`SELECT id, cnpj, legal_name, address, created_at FROM supermarkets
		 ORDER BY legal_name, id
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]domain.Supermarket, 0, limit)

	for rows.Next() {
		s := domain.Supermarket{}
		if err := rows.Scan(&s.ID, &s.CNPJ, &s.LegalName, &s.Address, &s.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}

	return out, total, rows.Err()
}

// RePointNfces moves every receipt of a supermarket onto another emitter (the
// admin merge "same company, two CNPJs" case); returns how many moved.
func (r supermarketRepo) RePointNfces(ctx context.Context, fromID, toID int64) (int64, error) {
	if fromID == toID {
		return 0, nil
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE nfces SET supermarket_id = $2 WHERE supermarket_id = $1`, fromID, toID)

	if err != nil {
		return 0, err
	}

	return tag.RowsAffected(), nil
}

// CountNfces reports how many receipts a supermarket still owns (the delete
// guard: a store with history cannot simply vanish).
func (r supermarketRepo) CountNfces(ctx context.Context, id int64) (int64, error) {
	var n int64
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM nfces WHERE supermarket_id = $1`, id).Scan(&n)
	return n, err
}

// Delete removes a supermarket row, or ErrNotFound. Callers must ensure no
// receipts reference it (CountNfces == 0) before calling.
func (r supermarketRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM supermarkets WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
