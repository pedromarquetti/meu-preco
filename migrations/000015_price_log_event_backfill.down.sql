-- Migration 000015_price_log_event_backfill.down.sql
--
-- The backfill cannot be reversed safely: once the audit events exist, they are
-- indistinguishable from events written by the application, and re-publishing
-- legacy assignments would undo the quarantine. This down migration is a
-- deliberate no-op so a roll-back never destroys audit history.
SELECT 1;
