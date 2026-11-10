-- This table represents the log of events for product prices:
-- A product might be merged/assigned, this allows admins to rollback values
CREATE TABLE price_log_event (
    id BIGSERIAL PRIMARY KEY,
    price_log_id BIGINT NOT NULL REFERENCES price_logs(id),
    from_product_id BIGINT NOT NULL REFERENCES products(id),
    to_product_id BIGINT NOT NULL REFERENCES products(id),
    user_id BIGINT NOT NULL REFERENCES users(id),
    action TEXT NOT NULL CHECK (action IN ('assign', 'approve', 'reject')),
    timestamp TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX price_log_event_price_log_idx
ON price_log_event (price_log_id, timestamp, id);
