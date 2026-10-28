
UPDATE commodity_synonyms SET prefix_expansion = NULL
WHERE prefix_expansion IS NOT NULL
;
ALTER TABLE commodity_synonyms DROP COLUMN IF EXISTS prefix_expansion;
