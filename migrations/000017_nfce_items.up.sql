-- Persist the normalized receipt lines on the receipt itself so an already
-- ingested receipt can be re-drafted without contacting SEFAZ again.
ALTER TABLE nfces ADD COLUMN items JSONB NOT NULL DEFAULT '[]'::JSONB;
