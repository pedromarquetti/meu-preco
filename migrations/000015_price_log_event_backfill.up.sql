-- Migration 000015_price_log_event_backfill.up.sql
--
-- Legacy manual assignments predate the event model: a uid_uncertain line was
-- linked to a product with needs_review = false and no audit event. Quarantine
-- those rows (they must be reviewed by an admin like new proposals) and write
-- the missing assign event. The NOT EXISTS guard makes the backfill idempotent:
-- a re-run finds no candidate and writes nothing, and rows already handled by
-- the Phase 2 commit path (which inserts the event and sets needs_review in the
-- same transaction) are left untouched.
WITH legacy AS (
    UPDATE price_logs pl
    SET needs_review = true
    WHERE
        pl.identity_status = 'uid_uncertain'
        AND pl.resolution_note = 'assign'
        AND pl.resolved_by IS NOT null
        AND pl.product_id IS NOT null
        AND NOT EXISTS (
            SELECT 1
            FROM price_log_event e
            WHERE
                e.price_log_id = pl.id
                AND e.action = 'assign'
        )
    RETURNING pl.id, pl.product_id, pl.resolved_by, pl.created_at
)

INSERT INTO price_log_event (
    price_log_id, from_product_id, to_product_id, user_id, action, timestamp
)
SELECT id, null, product_id, resolved_by, 'assign', COALESCE(created_at, NOW())
FROM legacy;
