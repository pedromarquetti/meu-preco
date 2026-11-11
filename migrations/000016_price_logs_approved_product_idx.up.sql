-- Migration 000016_price_logs_approved_product_idx.up.sql
--
-- Create a partial index to optimize searches that filter by approved price logs.
CREATE INDEX price_logs_approved_product_idx
ON price_logs (product_id)
WHERE needs_review = false AND product_id IS NOT null;
