INSERT INTO commodity_synonyms
(synonym, canonical_name, category, unit, source) VALUES
(
    'qj mussarela',
    'queijo mussarela',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'qjo mussarela',
    'queijo mussarela',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'qjomussarela',
    'queijo mussarela',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'qjmussarela',
    'queijo mussarela',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
('qj prato', 'queijo prato', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('qjo prato', 'queijo prato', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('qjprato', 'queijo prato', 'laticinios', 'KG', 'merged form per user'),
(
    'qj parmesao',
    'queijo parmesao',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'qjparmesao',
    'queijo parmesao',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'qjo parmesao',
    'queijo parmesao',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
(
    'qj parmesao vigor',
    'queijo parmesao',
    'laticinios',
    'KG',
    'CONAB PROHORT / CEAGESP'
),
('qj brie', 'queijo brie', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('qjo brie', 'queijo brie', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
(
    'qjo brie vigor',
    'queijo brie',
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
)
ON CONFLICT (synonym) DO NOTHING;
