package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"meupreco/internal/domain"
)

type productRepo struct{ db queryer }

func (r productRepo) Create(ctx context.Context, p *domain.Product) error {
	err := r.db.QueryRow(ctx,
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
	err := r.db.QueryRow(ctx,
		`SELECT id, product_key, gtin, name, brand, unit, canonical_stem, needs_review, created_by, created_at
		 FROM products WHERE id = $1`, id,
	).Scan(&p.ID, &p.ProductKey, &p.GTIN, &p.Name, &p.Brand, &p.Unit,
		&p.CanonicalStem, &p.NeedsReview, &p.CreatedBy, &p.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return p, nil
}

const productColumns = `id, product_key, gtin, name, brand, unit, canonical_stem, needs_review, created_by, created_at`

// scanProduct decodes a product row; pass sim!=nil to also read the trailing
// similarity column used by rank-ordered queries.
func (r productRepo) scanProduct(row pgx.Row, sim *float64) (*domain.Product, error) {
	p := &domain.Product{}
	dests := []any{&p.ID, &p.ProductKey, &p.GTIN, &p.Name, &p.Brand, &p.Unit,
		&p.CanonicalStem, &p.NeedsReview, &p.CreatedBy, &p.CreatedAt}

	if sim != nil {
		dests = append(dests, sim)
	}

	err := row.Scan(dests...)
	if err != nil {
		return nil, translateError(err)
	}

	return p, nil
}

// SearchFiltered implements domain.ProductStore.SearchFiltered: it filters the
// catalog with the non-empty ProductFilter fields, never exposing needs_review
// rows, and returns the page plus its total for stable pagination. The two
// queries share the same WHERE so the count always matches what a follow-up
// page would yield.
func (r productRepo) SearchFiltered(ctx context.Context, f domain.ProductFilter, limit, offset int) ([]domain.Product, int64, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	wheres := []string{"needs_review = false"}
	args := []any{}

	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, q)
		n := len(args)
		sim := fmt.Sprintf(
			"greatest(similarity(name, $%d), similarity(COALESCE(brand, ''), $%d), similarity(COALESCE(canonical_stem, ''), $%d))",
			n, n, n)
		wheres = append(wheres, sim+" > 0.1")
	}
	if gtin := strings.TrimSpace(f.GTIN); gtin != "" {
		args = append(args, gtin)
		n := len(args)

		// gtin is stored 14-digit zero-padded; strip the pad so a bare barcode
		// prefix (e.g. "7831") still matches "07891000412855".
		wheres = append(wheres, fmt.Sprintf(`ltrim(COALESCE(gtin, ''), '0') LIKE $%d || '%%'`, n))
	}

	if unit := strings.TrimSpace(f.Unit); unit != "" {
		args = append(args, unit)
		n := len(args)
		wheres = append(wheres, fmt.Sprintf("unit = $%d", n))
	}

	if brand := strings.TrimSpace(f.Brand); brand != "" {
		args = append(args, brand, brand)
		n := len(args)
		wheres = append(wheres, fmt.Sprintf("(brand ILIKE '%%' || $%d || '%%' OR name ILIKE '%%' || $%d || '%%')", n-1, n))
	}

	where := strings.Join(wheres, " AND ")
	order := "name"
	selectExtra := ""
	withSim := false
	if strings.Contains(where, "similarity") {
		order = "sim DESC, name"
		withSim = true
		selectExtra = `, greatest(
			similarity(name, $1),
			similarity(COALESCE(brand, ''), $1),
			similarity(COALESCE(canonical_stem, ''), $1)
		) AS sim`
	}

	var total int64
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM products WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	countArgs := append(append([]any{}, args...), limit, offset)
	rows, err := r.db.Query(ctx,
		`SELECT `+productColumns+selectExtra+` FROM products WHERE `+where+`
		 ORDER BY `+order+`
		 LIMIT $`+fmt.Sprint(len(args)+1)+` OFFSET $`+fmt.Sprint(len(args)+2), countArgs...)
	if err != nil {
		return nil, 0, err
	}

	defer rows.Close()

	out := make([]domain.Product, 0, limit)
	for rows.Next() {
		var sim *float64
		if withSim {
			sim = new(float64)
		}
		p, err := r.scanProduct(rows, sim)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}

	return out, total, rows.Err()
}

// Update applies the non-nil patch fields to a product (admin curation).
// Brand set with an explicit empty pointer clears the column.
func (r productRepo) Update(ctx context.Context, id int64, patch domain.ProductPatch) error {
	sets := []string{}
	args := []any{id}
	if patch.Name != nil {
		args = append(args, *patch.Name)
		sets = append(sets, fmt.Sprintf("name = $%d", len(args)))
	}
	if patch.Brand != nil {
		args = append(args, *patch.Brand)
		sets = append(sets, fmt.Sprintf("brand = NULLIF($%d, '')", len(args)))
	}
	if patch.Unit != nil {
		args = append(args, *patch.Unit)
		sets = append(sets, fmt.Sprintf("unit = $%d", len(args)))
	}
	if len(sets) == 0 {
		return nil
	}

	tag, err := r.db.Exec(ctx,
		`UPDATE products SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes a product row. The caller (service) re-points or purges its
// price_logs first; with rows still referencing the product the FK aborts.
func (r productRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM products WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r productRepo) GetByKey(ctx context.Context, key string) (*domain.Product, error) {
	p := &domain.Product{}
	err := r.db.QueryRow(ctx,
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
	err := r.db.QueryRow(ctx,
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

	rows, err := r.db.Query(ctx,
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

	rows, err := r.db.Query(ctx, sql.String(), args...)
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
	err := r.db.QueryRow(ctx,
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
