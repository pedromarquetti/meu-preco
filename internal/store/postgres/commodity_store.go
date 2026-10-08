package store

import (
	"context"

	"meupreco/internal/domain"
)

type synonymRepo struct{ s *Store }

func (r synonymRepo) Create(ctx context.Context, c *domain.CommoditySynonym) error {
	err := r.s.pool.QueryRow(ctx,
		`INSERT INTO commodity_synonyms (synonym, canonical_name, category, unit, source)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, created_at`,
		c.Synonym, c.CanonicalName, c.Category, c.Unit, c.Source,
	).Scan(&c.ID, &c.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

func (r synonymRepo) Canonicalize(ctx context.Context, synonym string) (*domain.CommoditySynonym, error) {
	c := &domain.CommoditySynonym{}
	err := r.s.pool.QueryRow(ctx,
		`SELECT id, synonym, canonical_name, category, unit, source, created_at
		 FROM commodity_synonyms WHERE synonym = $1`, synonym,
	).Scan(&c.ID, &c.Synonym, &c.CanonicalName, &c.Category, &c.Unit, &c.Source, &c.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return c, nil
}
