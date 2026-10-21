package store

import (
	"context"

	"meupreco/internal/domain"
)

type brandRepo struct{ db queryer }

func (r brandRepo) List(ctx context.Context) ([]domain.CommodityBrand, error) {
	rows, err := r.db.Query(ctx,
		`SELECT brand, display, created_at FROM commodity_brands ORDER BY brand`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.CommodityBrand
	for rows.Next() {
		var b domain.CommodityBrand
		if err := rows.Scan(&b.Brand, &b.Display, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
