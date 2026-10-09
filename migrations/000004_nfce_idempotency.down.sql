-- Restore the original previews shape: a globally-unique idempotency_key
-- column. Under the new schema the same receipt may have many preview rows
-- (one live draft per user), so a plain backfill from access_key would
-- duplicate — too many rows share a key. Derive a guaranteed-unique value
-- from the row id instead (still traceable to the carve-out it replaced).
DROP INDEX IF EXISTS previews_user_access_pending_idx;
ALTER TABLE previews ADD COLUMN idempotency_key TEXT;
UPDATE previews SET idempotency_key = access_key || ':' || id
WHERE idempotency_key IS NULL
;
ALTER TABLE previews ALTER column idempotency_key SET NOT NULL;

ALTER TABLE previews 
ADD CONSTRAINT previews_idempotency_key_key UNIQUE (idempotency_key);

CREATE INDEX previews_idempotency_key_idx ON previews (idempotency_key);

