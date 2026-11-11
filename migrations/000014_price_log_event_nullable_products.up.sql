-- Migration 000014_price_log_event_nullable_products.up.sql
--
-- A price-log event references products optionally: an initial uncertain line
-- has no prior product, and a rejection can concern a proposal with no target.
-- The original event table declared both columns NOT NULL; relax them so
-- assign events (from_product_id NULL) and unresolved decisions are recordable.
ALTER TABLE price_log_event
ALTER COLUMN from_product_id DROP NOT NULL,
ALTER COLUMN to_product_id DROP NOT NULL;
