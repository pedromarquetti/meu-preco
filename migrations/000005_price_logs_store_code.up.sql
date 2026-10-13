-- the admin needs-review queue shows the store's own product code
-- (cProd) next to the raw xProd, so the price_logs row must keep it. Also an
-- index that serves the needs-review queue (partial: only rows waiting on an
-- admin) and the product price-curve read (product_id + issued_at).

ALTER TABLE price_logs ADD COLUMN store_code TEXT;

CREATE INDEX price_logs_store_code_idx ON price_logs (store_code);

CREATE INDEX price_logs_review_idx
ON price_logs (created_at DESC)
WHERE needs_review = true;
 
