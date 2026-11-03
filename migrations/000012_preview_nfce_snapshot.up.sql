ALTER TABLE previews ADD COLUMN receipt_snapshot JSONB;
CREATE INDEX IF NOT EXISTS idx_previews_receipt_snapshot ON previews USING gin (
receipt_snapshot
);
