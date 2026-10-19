--  Creates every table in the data model exactly as
-- documented in PROJECT.md. Everything money-shaped is NUMERIC, ids are
-- BIGINT, and identity_status is a VARCHAR with a CHECK so it can evolve
-- without a PG enum migration.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE users (
id            BIGSERIAL PRIMARY KEY,
name          TEXT NOT NULL UNIQUE,
email         TEXT NOT NULL UNIQUE,
password_hash TEXT NOT NULL,
role          TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
CONSTRAINT users_name_not_blank CHECK (btrim(name) <> '')
);

CREATE TABLE supermarkets (
id         BIGSERIAL PRIMARY KEY,
cnpj       TEXT NOT NULL UNIQUE,
legal_name TEXT NOT NULL,
address    TEXT,
created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A GTIN barcode normalized to the GS1 zero-padded 14-digit form, or NULL for
-- un-barcoded goods (fresh produce, bulk). normalize_gtin() is applied on
-- write by the trigger below; raw 8/12/13-digit GTINs are padded to 14.
CREATE FUNCTION normalize_gtin(text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT AS
$fn$
    SELECT CASE WHEN $1 ~ '^\d{8}$' OR $1 ~ '^\d{12}$' OR $1 ~ '^\d{13}$' OR $1 ~ '^\d{14}$'
                THEN lpad(regexp_replace($1, '\D', '', 'g'), 14, '0')
                ELSE NULL END
$fn$;

CREATE TABLE products (
id             BIGSERIAL PRIMARY KEY,
-- gtin(14) or sha256(canonical_stem|unit)
product_key    TEXT NOT NULL UNIQUE,
-- normalized to 14 digits by trigger; nullable
gtin           TEXT,
name           TEXT NOT NULL,
brand          TEXT,
unit           TEXT NOT NULL,
canonical_stem TEXT,
needs_review   BOOLEAN NOT NULL DEFAULT false,
created_by     BIGINT REFERENCES users(id),
created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
CONSTRAINT products_gtin_check CHECK (gtin IS NULL OR gtin ~ '^\d{14}$')
);

CREATE INDEX products_trgm_idx
ON products USING gin (name gin_trgm_ops,
brand gin_trgm_ops,
canonical_stem gin_trgm_ops);

CREATE FUNCTION products_normalize_gtin() RETURNS trigger
LANGUAGE plpgsql AS
$fn$
BEGIN
    NEW.gtin := normalize_gtin(NEW.gtin);
    IF NEW.gtin IS NULL AND NEW.product_key ~ '^\d{14}$' THEN
        NEW.gtin := NEW.product_key;
    END IF;
    RETURN NEW;
END
$fn$;

CREATE TRIGGER products_normalize_gtin_before
BEFORE INSERT OR UPDATE ON products
FOR EACH ROW EXECUTE FUNCTION products_normalize_gtin();

CREATE TABLE nfces (
id             BIGSERIAL PRIMARY KEY,
access_key     TEXT NOT NULL UNIQUE,   -- 44-digit chave de acesso
supermarket_id BIGINT NOT NULL REFERENCES supermarkets(id),
number         TEXT NOT NULL,
series         TEXT NOT NULL,
issued_at      TIMESTAMPTZ,
protocol       TEXT,
environment    TEXT,
total          NUMERIC(14,2) NOT NULL DEFAULT 0,
discount       NUMERIC(14,2) NOT NULL DEFAULT 0,
paid_value     NUMERIC(14,2) NOT NULL DEFAULT 0,
payment_method TEXT,
coo            TEXT,
-- stripped DANFE/XML for audit and re-play
payload_raw    TEXT,
created_by     BIGINT REFERENCES users(id),
created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX nfces_issued_at_idx ON nfces (issued_at);

CREATE TABLE price_logs (
id                   BIGSERIAL PRIMARY KEY,
nfce_id              BIGINT NOT NULL REFERENCES nfces(id),
product_id           BIGINT REFERENCES products(id),
line                 INT NOT NULL,
description_snapshot TEXT NOT NULL,
qty                  NUMERIC(14,4) NOT NULL,
unit                 TEXT NOT NULL,
normalized_unit      TEXT,
price_per_unit       NUMERIC(14,6),
unit_price           NUMERIC(14,2) NOT NULL,
total_price          NUMERIC(14,2) NOT NULL,
identity_status      TEXT NOT NULL CHECK (identity_status IN (
'gtin_known',
'gtin_new',
'commodity_fingerprint',
'uid_uncertain',
'parse_error'
)),
needs_review         BOOLEAN NOT NULL DEFAULT false,
resolved_by          BIGINT REFERENCES users(id),
resolution_note      TEXT,
created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
CONSTRAINT price_logs_nfce_line_unique UNIQUE (nfce_id, line)
);

CREATE INDEX price_logs_product_idx   ON price_logs (product_id);
CREATE INDEX price_logs_nfce_idx      ON price_logs (nfce_id, created_at);

CREATE TABLE previews (
id              BIGSERIAL PRIMARY KEY,
user_id         BIGINT NOT NULL REFERENCES users(id),
access_key      TEXT NOT NULL,
plan_items      JSONB NOT NULL DEFAULT '[]'::jsonb,
plan_totals     JSONB,
status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending',
'committed',
'expired')),
idempotency_key TEXT NOT NULL UNIQUE,
ttl             TIMESTAMPTZ NOT NULL,
created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX previews_ttl_idx            ON previews (ttl);
CREATE INDEX previews_idempotency_key_idx ON previews (idempotency_key);

CREATE TABLE commodity_synonyms (
id             BIGSERIAL PRIMARY KEY,
synonym        TEXT NOT NULL UNIQUE,
canonical_name TEXT NOT NULL,
category       TEXT,
unit           TEXT,
source         TEXT,
created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX commodity_synonyms_trgm_idx
ON commodity_synonyms USING gin (synonym gin_trgm_ops,
canonical_name gin_trgm_ops);

