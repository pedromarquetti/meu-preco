package store

import (
	"context"

	"meupreco/internal/domain"
)

type productRepo struct{ s *Store }

func (r productRepo) Create(ctx context.Context, p *domain.Product) error {
	err := r.s.pool.QueryRow(ctx,
		`INSERT INTO products (product_key, gtin, name, brand, unit, canonical_stem, needs_review, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, gtin, created_at`,
		p.ProductKey, p.GTIN, p.Name, p.Brand, p.Unit, p.CanonicalStem, p.NeedsReview, p.CreatedBy,
	).Scan(&p.ID, &p.GTIN, &p.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

func (r productRepo) GetByID(ctx context.Context, id int64) (*domain.Product, error) {
	p := &domain.Product{}
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, product_key, gtin, name, brand, unit, canonical_stem, needs_review, created_by, created_at
		 FROM products WHERE id = $1`, id,
	).Scan(&p.ID, &p.ProductKey, &p.GTIN, &p.Name, &p.Brand, &p.Unit,
		&p.CanonicalStem, &p.NeedsReview, &p.CreatedBy, &p.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return p, nil
}

func (r productRepo) GetByKey(ctx context.Context, key string) (*domain.Product, error) {
	p := &domain.Product{}
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, product_key, gtin, name, brand, unit, canonical_stem, needs_review, created_by, created_at
		 FROM products WHERE product_key = $1`, key,
	).Scan(&p.ID, &p.ProductKey, &p.GTIN, &p.Name, &p.Brand, &p.Unit,
		&p.CanonicalStem, &p.NeedsReview, &p.CreatedBy, &p.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return p, nil
}

func (r productRepo) GetByGTIN(ctx context.Context, gtin string) (*domain.Product, error) {
	p := &domain.Product{}
	// normalize_gtin keeps the stored value canonical (14 digits); the lookup
	// normalizes the bare input the same way.
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, product_key, gtin, name, brand, unit, canonical_stem, needs_review, created_by, created_at
		 FROM products WHERE gtin = normalize_gtin($1)`, gtin,
	).Scan(&p.ID, &p.ProductKey, &p.GTIN, &p.Name, &p.Brand, &p.Unit,
		&p.CanonicalStem, &p.NeedsReview, &p.CreatedBy, &p.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return p, nil
}

// Search ranks products by pg_trgm similarity across name, brand and
// canonical_stem. The GIN trigram index makes this a fast word-based lookup;
// unaccented stems are seeded so "limao" finds "limão tahiti".
func (r productRepo) Search(ctx context.Context, query string, limit, offset int) ([]domain.Product, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	const score = `greatest(
		similarity(name, $1),
		similarity(COALESCE(brand, ''), $1),
		similarity(COALESCE(canonical_stem, ''), $1)
	)`

	rows, err := r.s.pool.Query(ctx,
		`SELECT id, product_key, gtin, name, brand, unit, canonical_stem, needs_review, created_by, created_at,
		        `+score+` AS sim
		 FROM products
		 WHERE `+score+` > 0.1
		 ORDER BY sim DESC, name
		 LIMIT $2 OFFSET $3`,
		query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.Product, 0, limit)
	for rows.Next() {
		var (
			p   domain.Product
			sim float64
		)
		if err := rows.Scan(&p.ID, &p.ProductKey, &p.GTIN, &p.Name, &p.Brand, &p.Unit,
			&p.CanonicalStem, &p.NeedsReview, &p.CreatedBy, &p.CreatedAt, &sim); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
