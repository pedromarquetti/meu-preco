# Product identity audit

The audit is read-only unless `--apply` is explicitly supplied:

```sh
go run ./cmd/audit-products
go run ./cmd/audit-products --fail-on-drift
go run ./cmd/audit-products --apply
```

Barcode products (`products.gtin` set, or a 14-digit numeric `product_key`) are
excluded from identity repair. Their key is the GTIN, not a commodity hash;
rewriting it would allow a duplicate row on the next barcode receipt. The audit
only classifies and repairs commodity rows.

Rows that drift and whose display name normalizes to no identity (packaging-only
names such as `Saco`) are labeled `manual_review_required` and are never
auto-repaired: the stored stem may be the only meaningful identity. A row whose
fallback identity already agrees with its columns is not reported as drift.

Review collisions before applying. Colliding rows are always skipped and listed
individually; the command never merges products or reassigns price logs.

For the known incident, inspect products 2 and 8, decide the correct target for
price logs 10 and 14, reassign them with
`POST /api/v1/admin/products/batch-assign`, and merge duplicates explicitly.
Then rerun the audit.

## Verification queries

The Go audit is authoritative because it applies the brand dictionary and unit
normalization. These SQL queries are coarse triage for the same checks.

Products with zero approved logs:

```sql
SELECT p.id, p.name, p.canonical_stem
FROM products p
WHERE NOT EXISTS (
  SELECT 1 FROM price_logs pl
  WHERE pl.product_id = p.id AND pl.needs_review = false
)
ORDER BY p.id;
```

Price logs whose description looks unlike the product stem (all rows):

```sql
SELECT pl.id, pl.description_snapshot, p.id, p.canonical_stem,
       similarity(pl.description_snapshot, p.canonical_stem) AS score
FROM price_logs pl
JOIN products p ON p.id = pl.product_id
WHERE p.canonical_stem IS NOT NULL
  AND similarity(pl.description_snapshot, p.canonical_stem) < 0.2
ORDER BY score, pl.id;
```

Approved logs whose description looks unlike the product stem:

```sql
SELECT pl.id, pl.description_snapshot, p.id, p.canonical_stem,
       similarity(pl.description_snapshot, p.canonical_stem) AS score
FROM price_logs pl
JOIN products p ON p.id = pl.product_id
WHERE pl.needs_review = false
  AND p.canonical_stem IS NOT NULL
  AND similarity(pl.description_snapshot, p.canonical_stem) < 0.2
ORDER BY score, pl.id;
```

Commodity rows with no stored stem:

```sql
SELECT p.id, p.name, p.canonical_stem, p.product_key
FROM products p
WHERE p.canonical_stem IS NULL OR p.canonical_stem = ''
ORDER BY p.id;
```
