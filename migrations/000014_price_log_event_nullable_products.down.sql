-- Migration 000014_price_log_event_nullable_products.down.sql
--
-- Restoring NOT NULL would require inventing product ids for events that
-- legitimately have none (for example the assign proposals this schema exists
-- to record). Rather than delete or rewrite audit history, this rollback is a
-- deliberate no-op; a full schema down still drops the table in migration 13.
SELECT 1;
