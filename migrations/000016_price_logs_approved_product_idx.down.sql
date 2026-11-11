-- Migration 000016_price_logs_approved_product_idx.down.sql
--
-- Drop the partial index created in migration 000016.
DROP INDEX IF EXISTS price_logs_approved_product_idx;
