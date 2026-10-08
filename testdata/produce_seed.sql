

INSERT INTO commodity_synonyms (
    synonym, canonical_name, category, unit, source
) VALUES
('limao tahiti', 'limao tahiti', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),
('limaotahiti', 'limao tahiti', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),
('limao', 'limao tahiti', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),

(
    'batata inglesa',
    'batata inglesa',
    'hortifruti',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'batata lisa',
    'batata inglesa',
    'hortifruti',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
('batata', 'batata inglesa', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),

('maca fuji', 'maca fuji', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('maca gala', 'maca gala', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('maca', 'maca fuji', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),

('banana prata', 'banana prata', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('banana', 'banana prata', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),

('cebola', 'cebola', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('alho', 'alho', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('tomate', 'tomate', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('pimentao', 'pimentao', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('cenoura', 'cenoura', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('alface', 'alface', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),
('abacaxi', 'abacaxi', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),
('mamao', 'mamao', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),
('melancia', 'melancia', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),

(
    'queijo mussarela',
    'queijo mussarela',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
('queijo prato', 'queijo prato', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('queijo minas', 'queijo minas', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('queijo', 'queijo mussarela', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('manteiga', 'manteiga', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),

(
    'frango inteiro',
    'frango inteiro',
    'acougue',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
('frango', 'frango inteiro', 'acougue', 'KG', 'CONAB PROHORT / CEAGESP'),
('patinho', 'patinho', 'acougue', 'KG', 'CONAB PROHORT / CEAGESP'),
('file mignon', 'file mignon', 'acougue', 'KG', 'CONAB PROHORT / CEAGESP'),
('pe de frango', 'pe de frango', 'acougue', 'KG', 'CONAB PROHORT / CEAGESP'),

('arroz branco', 'arroz branco', 'mercearia', 'KG', 'CONAB PROHORT / CEAGESP'),
(
    'feijao carioca',
    'feijao carioca',
    'mercearia',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'acucar refinado',
    'acucar refinado',
    'mercearia',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
('oleo de soja', 'oleo de soja', 'mercearia', 'LT', 'CONAB PROHORT / CEAGESP')
ON CONFLICT (synonym) DO NOTHING;
