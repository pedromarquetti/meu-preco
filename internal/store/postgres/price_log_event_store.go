package store

import (
	"context"

	"meupreco/internal/domain"
)

// priceLogEventRepo is the append-only repository for price-log actions.
type priceLogEventRepo struct{ db queryer }

// Create inserts one immutable event and loads the database-generated id and
// timestamp back into e. Invalid actions are rejected by the table's CHECK
// constraint; the error is returned to the caller, never swallowed.
func (r priceLogEventRepo) Create(ctx context.Context, e *domain.PriceLogEvent) error {
	err := r.db.QueryRow(ctx,
		`INSERT INTO price_log_event (price_log_id, from_product_id, to_product_id, user_id, action)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, timestamp`,
		e.PriceLogID, e.FromProductID, e.ToProductID, e.UserID, e.Action,
	).Scan(&e.ID, &e.Timestamp)

	if err != nil {
		return translateError(err)
	}
	return nil
}
