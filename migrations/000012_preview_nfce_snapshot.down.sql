ALTER TABLE previews DROP COLUMN receipt_snapshot;
DROP INDEX IF EXISTS idx_previews_receipt_snapshot;
