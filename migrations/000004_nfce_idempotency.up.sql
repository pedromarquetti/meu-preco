-- previews are idempotent by the receipt's access key instead of a
-- client-generated idempotency_key: one live pending draft per user per
-- receipt, enforced by a partial unique index. committed/expired rows are
-- freed by the write-time GC, so the same (user, access_key) can be drafted
-- again later without colliding.
ALTER TABLE previews DROP COLUMN idempotency_key;
DROP INDEX IF EXISTS previews_idempotency_key_idx;

CREATE UNIQUE INDEX previews_user_access_pending_idx
ON previews (user_id, access_key) WHERE status = 'pending';

