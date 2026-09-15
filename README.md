# MeuPreço

Backend-only Go service that tracks Brazilian supermarket prices from **NFC-e**
(Nota Fiscal de Consumidor Eletrônica) receipts.

Given an NFC-e access key / QR-code payload, the server fetches the receipt from
the SEFAZ public consult portal, parses the product list and prices, and logs a
price record to a shared database — then serves price history and
per-supermarket comparisons through a JSON API. Prices are **shared across all
users**; users contribute logs and read everything.

## Goals

- **Crowdsourced price tracking** — any user can submit a receipt (via the
  QR-code barcode) and immediately read price history and supermarket
  comparisons for the products inside it.
- **Reliable product identity** — the canonical, cross-supermarket key is the
  **GTIN** barcode; un-barcoded goods (fresh produce, bulk) fall back to a
  deterministic **commodity key**; ambiguous matches are flagged for admin
  review instead of polluting the catalog.
- **No silent writes** — receipts go through a two-phase **preview → confirm**
  flow with human-readable per-line explanations; `uid_uncertain` items are
  skipped by default unless the user assigns a known product or marks them for
  review.
- **Idempotent, correct ingestion** — re-submitting the same receipt is a no-op;
  a price log exists at most once per unique `(access key, line)`.
- **Polite and certificate-free scraping** — the server, never the browser,
  talks to SEFAZ, throttled and with backoff; external digital certificates are
  explicitly out of scope. Captcha challenges are proxied to the user instead of
  failing the request.
- **Backend-only, contract-driven** — a separate SPA consumes the versioned
  OpenAPI contract; scraper, auth, and storage stay out of the frontend
  toolchain.
- **Free and open data sources only** — enrichment uses Open Food Facts (BR),
  CONAB PROHORT / CEAGESP cotas, and Wikidata; paid options such as
  CadastroProduto / GS1 Brasil are documented but never required.

## How it works

```
QR-code payload (chave|versao|tpAmb)
        │  POST /api/v1/nfce/submit
        ▼
   server ──► SEFAZ portal (throttled, cookie jar, captcha-aware)
        │
        ▼
   parse DANFE (products, prices, supermarket, totals)
        │
        ▼
   identity resolution (GTIN / commodity key) + itemized preview
        │        POST /api/v1/price-logs  (confirm)
        ▼
   price_logs ──► products history, supermarket comparison API
```

## Stack

- Go 1.26 single binary — `chi` router.
- PostgreSQL + `pg_trgm` TODO: implement PostgreSQL.
- `golang-jwt/v5` + bcrypt.
- `goquery`.
- structured `log/slog`.
- `golang-migrate`.
- `testcontainers-go`.
- HTTP handler → service → store layering with interfaces keeps the scraper and
  storage independently testable.

## Current status

This is still a very early project!

## Getting started

### Run with make

```sh
make run                     # boots the API on :8080
curl localhost:8080/healthz  # {"status":"ok"}
```

```sh
make test     # go test ./... -race
make lint     # gofmt + go vet (+ staticcheck if installed)
make build    # bin/server
```

### env

The server expects some env. vars to be set.

| Var             | Required | Default | Accepted values                         |
| --------------- | -------- | ------- | --------------------------------------- |
| PORT            | no       | 8080    | 1–65535                                 |
| LOG_LEVEL       | no       | info    | `debug` \| `info` \| `warn` \| `error`  |
| LOG_FORMAT      | no       | pretty  | `pretty` \| `json`                      |
| CORS_ORIGINS    | no       | \*      | comma-separated origins, or `*`         |
| SEFAZ_FETCH_RPS | no       | 1       | integer ≥ 1                             |
| SEFAZ_BASE_URL  | no       | SP prod | https URL (test/proxy endpoint)         |
| JWT_ACCESS_TTL  | no       | 15m     | Go duration (e.g. `15m`)                |
| JWT_REFRESH_TTL | no       | 720h    | Go duration (e.g. `720h`)               |
| DATABASE_URL    | Phase 2  | —       | `postgres://…` (required when DB lands) |
| JWT_SECRET      | Phase 3  | —       | ≥ 32 chars (required when auth lands)   |

Secrets (`JWT_SECRET`, `DATABASE_URL`) are only ever set via the environment —
never committed.

### Container image:

```sh
docker build -t meupreco .
docker run --rm -p 8080:8080 meupreco
```

TODO: implement docker-compose that pulls dockerhub image?

## Documentation

- [PROJECT.md](PROJECT.md) — full design: receipt ingestion flow, product
  identity strategy, data sources, data model, API, and the phased
  implementation plan.
- `api/openapi.yaml` — versioned API contract (added in Phase 8; the SPA
  consumes only this).

## Non-goals

Multi-UF SEFAZ portals, XML-path parsing, and the frontend SPA are explicitly
out of scope for the current plan — they become a follow-up plan after Phase 8.
