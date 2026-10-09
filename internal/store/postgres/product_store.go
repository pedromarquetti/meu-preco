package store

import (
	"context"
	"fmt"
	"strings"

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

// UpsertMany batch-inserts products keyed on product_key. Existing rows are
// updated in place (OFF re-runs refresh name/brand/unit, they never duplicate).
// The count returned is how many rows were created new. Batches are chunked so
// the statement stays far below Postgres' parameter limit.
func (r productRepo) UpsertMany(ctx context.Context, products []*domain.Product) (int, error) {
	const chunkSize = 100

	created := 0
	for start := 0; start < len(products); start += chunkSize {
		end := start + chunkSize
		if end > len(products) {
			end = len(products)
		}
		n, err := r.upsertChunk(ctx, products[start:end])
		if err != nil {
			return created, err
		}
		created += n
	}
	return created, nil
}

func (r productRepo) upsertChunk(ctx context.Context, chunk []*domain.Product) (int, error) {
	sql := strings.Builder{}
	sql.WriteString(`INSERT INTO products (product_key, gtin, name, brand, unit, created_by) VALUES `)

	args := make([]any, 0, len(chunk)*6)
	for i, p := range chunk {
		if i > 0 {
			sql.WriteString(", ")
		}
		base := i * 6
		fmt.Fprintf(&sql, "($%d, $%d, $%d, $%d, $%d, $%d)", base+1, base+2, base+3, base+4, base+5, base+6)
		args = append(args, p.ProductKey, p.GTIN, p.Name, p.Brand, p.Unit, p.CreatedBy)
	}
	// ON CONFLICT updates tend to leave created_by nulled; keep the original
	// created_by when present and refresh the mutable product fields.
	sql.WriteString(`
		ON CONFLICT (product_key) DO UPDATE SET
			gtin = COALESCE(EXCLUDED.gtin, products.gtin),
			name = EXCLUDED.name,
			brand = COALESCE(EXCLUDED.brand, products.brand),
			unit = EXCLUDED.unit
		RETURNING (xmax = 0) AS inserted`)

	rows, err := r.s.pool.Query(ctx, sql.String(), args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var inserted bool
		if err := rows.Scan(&inserted); err != nil {
			return 0, err
		}
		if inserted {
			n++
		}
	}
	return n, rows.Err()
}

// NearestMatch ranks products by pg_trgm similarity to the stem across name,
// brand and canonical_stem and returns the single best row whose score clears
// threshold, together with the score. ErrNotFound when nothing matches.
func (r productRepo) NearestMatch(ctx context.Context, stem string, threshold float64) (*domain.Product, float64, error) {
	const score = `greatest(
		similarity(name, $1),
		similarity(COALESCE(brand, ''), $1),
		similarity(COALESCE(canonical_stem, ''), $1)
	)`

	p := &domain.Product{}
	var sim float64
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, product_key, gtin, name, brand, unit, canonical_stem, needs_review, created_by, created_at,
		        `+score+` AS sim
		 FROM products
		 WHERE `+score+` >= $2
		 ORDER BY sim DESC, name
		 LIMIT 1`,
		stem, threshold,
	).Scan(&p.ID, &p.ProductKey, &p.GTIN, &p.Name, &p.Brand, &p.Unit,
		&p.CanonicalStem, &p.NeedsReview, &p.CreatedBy, &p.CreatedAt, &sim)
	if err != nil {
		return nil, 0, translateError(err)
	}
	return p, sim, nil
}
