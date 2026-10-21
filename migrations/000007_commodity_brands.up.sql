-- Curated brand vocabulary for GTIN-less commodity identity. A receipt line
-- like "QJO BRIE VIGOR KG" is stripped of the known brand ("Vigor") before the
-- taxonomy lookup, and the brand then scopes the commodity product_key so
-- Tirolez brie and Vigor brie no longer collapse onto one product.
--
-- brand is the normalized lowercase key (matching how descriptions are
-- normalized: lower-case, accent-stripped); display is the spelling stored on
-- products.brand. Idempotent: re-running is a no-op.

CREATE TABLE commodity_brands (
    brand TEXT PRIMARY KEY,
    display TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO commodity_brands (brand, display) VALUES
-- Laticínios e queijos
('tirolez', 'Tirolez'),
('vigor', 'Vigor'),
('catupiry', 'Catupiry'),
('polenghi', 'Polenghi'),
('piracanjuba', 'Piracanjuba'),
('itambe', 'Itambé'),
('scala', 'Scala'),
('betania', 'Betânia'),
('faixa azul', 'Faixa Azul'),
('serrabella', 'Serrabella'),
('jung', 'Jung'),
('pocos de caldas', 'Poços de Caldas'),
('aviacao', 'Aviação'),
('batavo', 'Batavo'),
('elege', 'Elegê'),
('parmalat', 'Parmalat'),
('danone', 'Danone'),
('nestle', 'Nestlé'),
('president', 'President'),
('qualita', 'Qualitá'),
('cativa', 'Cativa'),
('frimesa', 'Frimesa'),
('qualy', 'Qualy'),
('claybom', 'Claybom'),
('doriana', 'Doriana'),
('becel', 'Becel'),
('delicia', 'Delícia'),

-- Açougue e frios
('sadia', 'Sadia'),
('perdigao', 'Perdigão'),
('seara', 'Seara'),
('friboi', 'Friboi'),
('aurora', 'Aurora'),
('marfrig', 'Marfrig'),
('minerva', 'Minerva'),
('swift', 'Swift'),

-- Mercearia
('camil', 'Camil'),
('tio joao', 'Tio João'),
('namorado', 'Namorado'),
('kicaldo', 'Kicaldo'),
('coqueiro', 'Coqueiro'),
('barilla', 'Barilla'),
('renata', 'Renata'),
('galo', 'Galo'),
('quero', 'Quero'),
('heinz', 'Heinz'),
('hellmanns', 'Hellmann''s'),
('arisco', 'Arisco'),
('sason', 'Sason'),
('maggi', 'Maggi'),
('knorr', 'Knorr'),
('ajinomoto', 'Ajinomoto'),
('fugini', 'Fugini'),
('tarantella', 'Tarantella'),
('cepera', 'Cepera'),
('predilecto', 'Predilecto'),
('adria', 'Adria'),
('vitarella', 'Vitarella'),
('isabela', 'Isabela'),
('marilan', 'Marilan'),
('piraque', 'Piraquê'),
('bauducco', 'Bauducco'),
('tostines', 'Tostines'),
('triunfo', 'Triunfo'),
('kelloggs', 'Kellogg''s'),
('mccain', 'McCain'),

-- Óleos e gorduras
('liza', 'Liza'),
('soya', 'Soya'),
('bunge', 'Bunge'),
('vila velha', 'Vila Velha'),
('soberano', 'Soberano'),

-- Café, açúcar e bebidas
('pilao', 'Pilão'),
('melitta', 'Melitta'),
('3 coracoes', '3 Corações'),
('caboclo', 'Caboclo'),
('nescafe', 'Nescafé'),
('pimpinela', 'Pimpinela'),
('santa clara', 'Santa Clara'),
('uniao', 'União'),
('guarani', 'Guarani'),
('caravelas', 'Caravelas'),
('del valle', 'Del Valle'),
('sufresh', 'Sufresh'),

-- Padaria
('pullman', 'Pullman'),
('seven boys', 'Seven Boys'),
('wickbold', 'Wickbold'),
('nutrella', 'Nutrella'),
('visconti', 'Visconti'),

-- Chocolates e doces
('lacta', 'Lacta'),
('garoto', 'Garoto'),
('cacau show', 'Cacau Show'),
('kopenhagen', 'Kopenhagen')
ON CONFLICT (brand) DO NOTHING;

-- The classifier now strips the brand token before canonicalizing, so the
-- brand-laden stems seeded in 000002 are no longer reached. Round out the
-- abbreviation vocabulary the live receipts use ("QJ BRIE TIROLEZ KG" leaves
-- "qj brie" once Tirolez is stripped).
INSERT INTO commodity_synonyms 
(synonym, canonical_name, category, unit, source) 
VALUES
('qj brie', 'queijo brie', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP'),
('brie', 'queijo brie', 'laticinios', 'KG', 'CONAB PROHORT / CEAGESP')
ON CONFLICT (synonym) DO NOTHING;
