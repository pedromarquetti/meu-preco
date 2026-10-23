-- commodity identities for GTIN-less goods (fresh
-- produce, bulk, cheeses…). Synonym vocabulary is normalized lowercase,
-- accent-stripped stems; canonical_name is the human-readable reference used
-- in admin curation. Idempotent: re-running is a no-op.

INSERT INTO commodity_synonyms (
    synonym, canonical_name, category, unit, source
) VALUES
-- Limão 
('limao tahiti', 'limao tahiti', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),
('limao thaiti', 'limao tahiti', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),
('limaotahiti', 'limao tahiti', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),
('limao', 'limao tahiti', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),
('limao siciliano', 'limao siciliano', 'hortifruti', 'UN', ''),
('limão siciliano', 'limao siciliano', 'hortifruti', 'UN', ''),
('limaosiciliano', 'limao siciliano', 'hortifruti', 'UN', ''),
('limao verde', 'limao verde', 'hortifruti', 'UN', ''),
('limaoverde', 'limao verde', 'hortifruti', 'UN', ''),
('limão verde', 'limao verde', 'hortifruti', 'UN', ''),
('limao galego', 'limao galego', 'hortifruti', 'UN', ''),
('limaogalego', 'limao galego', 'hortifruti', 'UN', ''),
('limão galego', 'limao galego', 'hortifruti', 'UN', ''),

-- mi
('milho verde', 'milho verde', 'hortifruti', 'UN', ''),
('milho fresco', 'milho verde', 'hortifruti', 'UN', 'synonym per user'),
('milho', 'milho verde', 'hortifruti', 'UN', 'generic fallback'),

-- Iogurte natural (laticinios) - brand ITAMBÉ extracted at parse time
('iogurte natural', 'iogurte natural', 
'laticinios', 'UN', 'produtos.json sample'),
('iogurte natural 170g', 'iogurte natural', 'laticinios', 'UN', 'variation'),
('iogurte', 'iogurte natural', 'laticinios', 'UN', 'generic fallback'),

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

-- Banana nanica / tipos de banana (hortifruti) - match first two words
('banana nanica', 'banana nanica', 'hortifruti', 'KG', 'produtos.json sample'),
('banana prata', 'banana prata', 'hortifruti', 'KG', 'distinct variety'),
('banana', 'banana prata', 'hortifruti', 'KG', 'generic fallback to prata'),
('banana maçã', 'banana maçã', 'hortifruti', 'KG', 'distinct variety'),
('banana ouro', 'banana ouro', 'hortifruti', 'KG', 'distinct variety'),
('banana da terra', 'banana da terra', 'hortifruti', 'KG', 'distinct variety'),
('banana prata', 'banana prata', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),


-- Laranja lima / tipos de laranja (hortifruti)
('laranja lima', 'laranja lima', 'hortifruti', 'KG', 'produtos.json sample'),
('laranja lima 1kg', 'laranja lima', 'hortifruti', 'KG', 'variation'),
('laranja', 'laranja lima', 'hortifruti', 'KG', 'generic fallback'),
('laranha lima branca', 'laranja lima branca', 'hortifruti', 'KG', 'distinct variety'),
('laranja lima amarela', 'laranja lima amarela', 'hortifruti', 'KG', 'distinct variety'),
('laranja pera', 'laranja pera', 'hortifruti', 'KG', 'distinct variety'),
('laranja natal', 'laranja natal', 'hortifruti', 'KG', 'distinct variety'),
('laranja baía', 'laranja baía', 'hortifruti', 'KG', 'distinct variety'),

-- Cebola nacional / tipos de cebola (hortifruti)
('cebola nacional', 'cebola nacional',
'hortifruti', 'KG', 'produtos.json sample CEBOLA NAC'),
('cebola', 'cebola nacional', 'hortifruti', 'KG', 'generic fallback per user'),
('cebola nac', 'cebola nacional', 'hortifruti', 'KG', 'generic fallback per user'),
('cebola roxa nacional', 'cebola roxa', 'hortifruti', 'KG', 'distinct variety'),
('cebola branca nacional', 'cebola branca',
'hortifruti', 'KG', 'distinct variety'),
('cebola roxa', 'cebola roxa', 'hortifruti', 'KG', 'accented form'),
('cebola branca', 'cebola branca', 'hortifruti', 'KG', 'accented form'),

('alho', 'alho', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('tomate', 'tomate', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),

('pimentao verde', 'pimentao verde', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('pimentao amarelo', 'pimentao amarelo', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('pimentao vermelho', 'pimentao vermelho', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),

('cenoura', 'cenoura', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),
('alface', 'alface', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),
('abacaxi', 'abacaxi', 'hortifruti', 'UN', 'CONAB PROHORT / CEAGESP'),

-- Mamão formosa / tipos de mamão (hortifruti)
('mamao formosa', 'mamão formosa', 'hortifruti', 'UN', 'produtos.json sample'),
('mamao', 'mamão formosa', 'hortifruti', 'UN', 'generic fallback per user'),
('mamão formosa', 'mamão formosa', 'hortifruti', 'UN', 'variation'),
('mamao papaia', 'mamão papaia', 'hortifruti', 'UN', 'distinct variety'),
('mamão papaia', 'mamão papaia', 'hortifruti', 'UN', 'accented form'),

('melancia', 'melancia', 'hortifruti', 'KG', 'CONAB PROHORT / CEAGESP'),

-- Queijos maybe 
-- TODO: The server should be able to parse/find these variations? we cannot guess all possible variations
('queijo mussarela','queijo mussarela','laticinios','KG','CONAB PROHORT / CEAGESP'),
('qj mussarela','queijo mussarela','laticinios','KG','CONAB PROHORT / CEAGESP'),
('qjo mussarela','queijo mussarela', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('qjomussarela','queijo mussarela', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('qjmussarela','queijo mussarela', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('queijo prato', 'queijo prato', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('qj prato', 'queijo prato', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('qjo prato', 'queijo prato', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('qjprato', 'queijo prato', 'laticinios', 'KG', 'merged form per user'),
('queijo minas', 'queijo minas', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('queijo minas padrao', 
'queijo minas', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('queijo', 'queijo mussarela', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('queijo parmesao','queijo parmesao','laticinios','KG','CONAB PROHORT / CEAGESP'),
('qj parmesao','queijo parmesao','laticinios','KG','CONAB PROHORT / CEAGESP'),
('qjparmesao','queijo parmesao','laticinios','KG','CONAB PROHORT / CEAGESP'),
('qjo parmesao','queijo parmesao','laticinios','KG','CONAB PROHORT / CEAGESP'),
('parmesao', 'queijo parmesao', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
(
    'qj parmesao vigor',
    'queijo parmesao',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),

('queijo brie', 'queijo brie', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('qjo brie', 'queijo brie', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
(
    'qjo brie vigor',
    'queijo brie',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),

(
    'queijo gorgonzola',
    'queijo gorgonzola',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'gorgonzola',
    'queijo gorgonzola',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'queijo mofo',
    'queijo gorgonzola',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'queijo azul',
    'queijo gorgonzola',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'queijo mofo azul',
    'queijo gorgonzola',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'queijo mofo azul tirolez',
    'queijo gorgonzola',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),

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
