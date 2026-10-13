DROP INDEX price_logs_review_idx;
DROP INDEX price_logs_store_code_idx;

ALTER TABLE price_logs DROP COLUMN store_code;

