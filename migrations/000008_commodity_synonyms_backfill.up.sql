-- Backfill the QUESO/J abbreviation stems used by the classifier once brand
-- words are stripped ("QJ BRIE TIROLEZ KG" -> "qj brie"). The brie variants are
-- already seeded by 000007; these two generic abbreviations were missing.
-- Idempotent: re-running is a no-op.

INSERT INTO commodity_synonyms (synonym, canonical_name, category, unit, source)
VALUES
('qj', 'queijo mussarela', 'laticinios', 'KG', 'backfill'),
('qjo', 'queijo mussarela', 'laticinios', 'KG', 'backfill')
ON CONFLICT (synonym) DO NOTHING;
