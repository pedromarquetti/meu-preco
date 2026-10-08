DROP TABLE IF EXISTS commodity_synonyms;
DROP TABLE IF EXISTS previews;
DROP TABLE IF EXISTS price_logs;
DROP TABLE IF EXISTS nfces;
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS supermarkets;
DROP TABLE IF EXISTS users;

DROP FUNCTION IF EXISTS products_normalize_gtin ();
DROP FUNCTION IF EXISTS normalize_gtin(text);

DROP EXTENSION IF EXISTS pg_trgm;

