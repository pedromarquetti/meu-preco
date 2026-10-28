-- Prefix expansion column for abbreviation synonyms (qj/qjo -> queijo) so that
-- glued inputs like "QJOPRATO" expand to the canonical identity before fuzzy match,
-- while exact-match rows remain unchanged and backward compatible. Idempotent:
-- re-running is a no-op; existing synonym rows are updated in-place with NULL =
-- legacy/exact-only semantics preserved by default (no new index required).

ALTER TABLE
commodity_synonyms ADD COLUMN IF NOT EXISTS prefix_expansion TEXT;

UPDATE commodity_synonyms 
SET prefix_expansion = 'queijo' WHERE synonym IN ('qj', 'qjo');

-- Identity prefixes support glued full words 
-- ("QUEIJO PRATO" -> exact match first, 
-- but "queijoprato" can still be resolved via expansion if needed). 
-- Safe because exact-match runs before prefix-expansion in the classifier.
UPDATE commodity_synonyms 
SET prefix_expansion = synonym WHERE synonym IN ('queijo');
