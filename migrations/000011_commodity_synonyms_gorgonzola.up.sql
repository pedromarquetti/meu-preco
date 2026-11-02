-- "mofo" and "azul" are common receipt words for gorgonzola cheese, but they
-- are ambiguous standalone (e.g. "mofo" could appear in unrelated descriptions).
-- Mark them as cheese aliases so the classifier only resolves them after a
-- cheese prefix like "qj"/"qjo"/"queijo" (e.g. "qj mofo" -> "queijo gorgonzola").
-- Idempotent: re-running is a no-op.
INSERT INTO commodity_synonyms (synonym, canonical_name, category, unit, source)
VALUES
('mofo', 'queijo gorgonzola', 'laticinios', 'KG', 'cheese_alias'),
('azul', 'queijo gorgonzola', 'laticinios', 'KG', 'cheese_alias')
ON CONFLICT (synonym) DO NOTHING;
