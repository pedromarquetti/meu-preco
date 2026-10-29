package store

import (
	"context"

	"meupreco/internal/domain"
)

type synonymRepo struct{ db queryer }

func (r synonymRepo) Create(ctx context.Context, c *domain.CommoditySynonym) error {
	err := r.db.QueryRow(ctx,
		`INSERT INTO commodity_synonyms (synonym, canonical_name, category, unit, source, prefix_expansion)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, created_at`,
		c.Synonym, c.CanonicalName, c.Category, c.Unit, c.Source, c.PrefixExpansion,
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
	err := r.db.QueryRow(ctx,
		`SELECT id, synonym, canonical_name, category, unit, source, created_at
		 FROM commodity_synonyms WHERE synonym = $1`, synonym,
	).Scan(&c.ID, &c.Synonym, &c.CanonicalName, &c.Category, &c.Unit, &c.Source, &c.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return c, nil
}

// MatchPrefix finds the longest synonym that prefixes stem and has non-nil prefix_expansion. Uses left() so %/_ never act as wildcards; char_length DESC guarantees qjo beats qj for glued inputs like "qjoprato". Returns ErrNotFound when no matching abbreviation exists (not an error condition).
func (r synonymRepo) MatchPrefix(ctx context.Context, stem string) (*domain.CommoditySynonym, error) {
	c := &domain.CommoditySynonym{}
	err := r.db.QueryRow(ctx,
		`SELECT id, synonym, canonical_name, category, unit, source, prefix_expansion, created_at
		 FROM commodity_synonyms
	  WHERE prefix_expansion IS NOT NULL AND synonym <> ''
	    AND left($1, char_length(synonym)) = synonym
	ORDER BY char_length(synonym) DESC LIMIT 1`, stem,
	).Scan(&c.ID, &c.Synonym, &c.CanonicalName, &c.Category, &c.Unit, &c.Source, &c.PrefixExpansion, &c.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	return c, nil
}
