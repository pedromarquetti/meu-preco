-- Migration 000013_price_log_event.down.sql
DROP INDEX IF EXISTS price_log_event_price_log_idx;

DROP TABLE IF EXISTS price_log_event;
