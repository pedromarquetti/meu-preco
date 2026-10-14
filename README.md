# MeuPreço

<!--toc:start-->

- [MeuPreço](#meupreço)
  - [Goals](#goals)
  - [How it works](#how-it-works)
  - [Stack](#stack)
  - [Features](#features)
  - [Getting started](#getting-started)
    - [Run with make](#run-with-make)
    - [env](#env)
    - [Hardening & operations](#hardening-operations)
    - [Container image:](#container-image)
  - [Documentation](#documentation)
  - [Inspiration](#inspiration)
  - [Non-goals](#non-goals)

<!--toc:end-->

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
  flow with human-readable per-line explanations; `uid_uncertain` items default
  to private `needs_review` rows (invisible to public history until an admin
  resolves them) and are discarded only when `skip` is requested explicitly.
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
- **Multi-UF by design** — Other states will be implemented in the future, for
  now, only receipts from _São Paulo_ are supported.

## How it works

```
QR-code payload / access_key
    │  POST /api/v1/nfce/new   (draft: fetch + classify, persists only a plan)
    ▼
registry dispatch ──► 501 unsupported_uf  (non-SP states today)
    │
    ▼
HTTPSource → SEFAZ portal (throttled, cookie jar, captcha-aware)
    │  ← exact scanned payload replayed for v2/offline variants
    │  └─► poral demands a captcha ──► 412 challenge_id + proxied no-store image
    │        user solves it via POST /api/v1/nfce/captcha → plan
    ▼
parse DANFE ("Consulta Resumida" family — products, prices, supermarket, totals)
    │
    ▼
identity resolution (GTIN / commodity key) + itemized plan
    │        POST /api/v1/nfce/submit  (confirm → writes atomically)
    ▼
price_logs ──► products history, supermarket comparison API
```

## Stack

- Go 1.26 single binary — `chi` router.
- PostgreSQL + `pg_trgm` (Postgres 18 running in Docker).
- `golang-jwt/v5` + bcrypt.
- `goquery`.
- structured `log/slog`.
- `golang-migrate` (embedded migrations).
- pgx v5 (`database/sql` + pool).
- HTTP handler → service → store layering with interfaces keeps the scraper and
  storage independently testable.

## Features

- [ ] Docker image — TODO: Docker will be implemented soon
- [x] API Docs — OpenAPI validation & JSON generation via GitHub Actions, GitHub
      Pages deployment, `make openapi-generate/check/diff/bundle/static` targets
- [x] Receipt ingestion pipeline — QR-code/Access key parsing, SEFAZ portal
      scraping (DANFE), HTML parsing for products/prices, two-phase commit
      (draft → submit), idempotent ingestion, draft preview with TTL
- [ ] Receipt parsing for other states — For now, it only supports São Paulo
- [x] Captcha handling — challenges proxied to user, state & image proxy
      endpoints, retry budget tracking
- [x] Product identity resolution — GTIN-based canonical keys, commodity
      fallback for unbarcoded goods, similarity scoring, preview item statuses
      (gtin_known, gtin_new, commodity_fingerprint, uid_uncertain, parse_error)
- [x] Product catalog API — search with fuzzy name/GTIN/unit/brand filters,
      product history with price curves, review queue for uncertain matches
- [x] Product administration — curation (name/brand/unit), guarded delete,
      merge, batch-review resolution, admin user management
- [x] Supermarket API — list emitters, fetch products with latest prices, single
      supermarket details
- [x] Supermarket administration — admin-only deletion with receipt guards,
      CNPJ-based merge
- [x] Authentication — register, login, refresh tokens, profile endpoint, bcrypt
      passwords, JWT access+refresh with rotation & reuse detection
- [x] Data enrichment — Open Food Facts BR integration, background seeding from
      bulk dumps, cache tracking, configurable via
      `ENRICH_OFF`/`MEUPRECO_LIVE_OFF`
- [x] Price log storage — shared across users, per-unit tracking with aggregates
      (min/avg/max/last), commodity code & CNPJ base extraction for supermarket
      comparisons
- [x] Background jobs — preview garbage collection, Open Food Facts cache
      management
- [x] Rate limiting — global per-IP token bucket, authenticated per-user bucket,
      429 with `Retry-After` header
- [x] Security headers — `X-Content-Type-Options`, `X-Frame-Options`,
      `Referrer-Policy`, `Cache-Control: no-store`
- [x] Health & metrics — `/healthz` liveness, `/readyz` readiness, `/metrics`
      with route counters, latency histogram, auth failures, OFF cache stats
- [x] Database & migrations — PostgreSQL with `pg_trgm`, `golang-migrate` with
      embedded migrations, initial schema + commodity synonyms seeding
- [x] Configuration — env var + `.env` file, configurable
      timeouts/retries/backoff for SEFAZ, CORS origins, log level/format
- [x] Operational features — structured `log/slog` logging, pgx v5 connection
      pool, throttled SEFAZ requests with exponential backoff, cookie jar for
      portal sessions
- [x] Testing — race detection (`go test -race`), stub stores, mem stores,
      Postgres integration tests, live OFF test
- [x] CI/CD — GitHub Actions for OpenAPI validation, JSON generation, test &
      lint workflows

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

# Database: migrate up/down against POSTGRES_* + DATABASE_NAME
export POSTGRES_USERNAME='dev' POSTGRES_PASSWORD='dev' DATABASE_NAME='meupreco'
make migrate-up
make migrate-down
make migrate-version

# Postgres integration tests use their own database ($MEUPRECO_TEST_DATABASE_URL)
export MEUPRECO_TEST_DATABASE_URL='postgres://dev:dev@localhost:5432/meupreco_test?sslmode=disable'
go test ./internal/store/...

# Import a bulk Open Food Facts dump (789/790 GTINs only, idempotent)
make seed-off ARGS=./openfoodfacts-products.jsonl.gz

# Opt-in live Open Food Facts BR test (hermetic runs skip it)
export MEUPRECO_LIVE_OFF=1
go test ./internal/enrich/... -run TestOFFLookupLive -v
```

### env

The server expects some env. vars to be set.

| Var                   | Required | Default   | Accepted values                                                        |
| --------------------- | -------- | --------- | ---------------------------------------------------------------------- |
| PORT                  | no       | 8080      | 1–65535                                                                |
| LOG_LEVEL             | no       | info      | `debug` \| `info` \| `warn` \| `error`                                 |
| LOG_FORMAT            | no       | pretty    | `pretty` \| `json`                                                     |
| CORS_ORIGINS          | no       | \*        | comma-separated origins, or `*`                                        |
| SEFAZ_FETCH_RPS       | no       | 1         | integer ≥ 1                                                            |
| SEFAZ_MAX_RETRIES     | no       | 3         | integer ≥ 1 (fetch attempts per receipt)                               |
| SEFAZ_TIMEOUT         | no       | 30s       | Go duration (per portal request)                                       |
| SEFAZ_BACKOFF_BASE    | no       | 200ms     | Go duration ≥ 0 (start of the exponential retry delay)                 |
| SEFAZ_BACKOFF_CAP     | no       | 2s        | Go duration ≥ `SEFAZ_BACKOFF_BASE` (cap per retry delay)               |
| SEFAZ_BASE_URL        | no       | SP prod   | https URL (overrides the SP QR consultation endpoint for test proxies) |
| PREVIEWS_TTL          | no       | 15m       | Go duration > 0 (pending draft lifetime)                               |
| PREVIEW_GC_INTERVAL   | no       | 5m        | Go duration > 0 (background sweep period)                              |
| RATE_LIMIT_IP_RPS     | no       | 5         | float ≥ 0 (0 disables the per-IP bucket)                               |
| RATE_LIMIT_IP_BURST   | no       | 300       | integer ≥ 0 (per-IP burst)                                             |
| RATE_LIMIT_USER_RPS   | no       | 20        | float ≥ 0 (0 disables the per-user bucket)                             |
| RATE_LIMIT_USER_BURST | no       | 1000      | integer ≥ 0 (per-user burst)                                           |
| ENRICH_OFF            | no       | false     | `true`/`false` (best-effort Open Food Facts enrichment of `gtin_new`)  |
| POSTGRES_USERNAME     | yes      | —         | postgres login (builds the DSN)                                        |
| POSTGRES_PASSWORD     | yes      | —         | postgres password (builds the DSN; never logged/committed)             |
| DATABASE_NAME         | yes      | —         | database name, e.g. `meupreco`                                         |
| POSTGRES_HOST         | no       | localhost | postgres host                                                          |
| POSTGRES_PORT         | no       | 5432      | postgres port (1–65535)                                                |
| JWT_ACCESS_TTL        | no       | 15m       | Go duration (e.g. `15m`)                                               |
| JWT_REFRESH_TTL       | no       | 720h      | Go duration (e.g. `720h`)                                              |
| JWT_SECRET            | yes      | —         | ≥ 32 chars; signs access + refresh tokens                              |
| ADMIN_EMAIL           | yes      | —         | seeds the first admin (role `admin`) at boot when `users` is empty     |
| ADMIN_PASSWORD        | yes      | —         | seeds the first admin password (bcrypt-hashed at boot)                 |

The DSN is built as `postgres://USER:PASS@HOST:PORT/DATABASE?sslmode=disable`.
Missing DB vars (or a too-short `JWT_SECRET`, or a missing `ADMIN_EMAIL`/
`ADMIN_PASSWORD`) abort startup with an explicit error. Secrets (DB password,
`JWT_SECRET`, `ADMIN_PASSWORD`) are only ever set via the environment — never
committed.

The initial admin is created **at boot, before the HTTP listener starts** (no
API call needed): the server hashes `ADMIN_PASSWORD` and inserts the admin the
first time the `users` table is empty. Every later boot is a no-op, so the admin
is already logged-in-able as soon as the server answers.

`make run` and `make migrate-*` automatically load `./.env` if present
(KEY=VALUE shell syntax; copy `.env.example` to `.env` and fill in real values).
Without `.env`, vars must be exported in the shell. In Docker the platform
injects environment directly (`env_file`/`-e`) — the compiled binary never reads
`.env` itself.

### Hardening & operations

- **Rate limiting** — a global per-IP token bucket guards the whole `/api/v1`
  tree; an authenticated per-user bucket is layered on the protected routes
  (keyed by the JWT subject). A throttled request answers `429` with the
  canonical envelope (`error.code = "rate_limited"`) and a `Retry-After` header.
  Set the relevant `RATE_LIMIT_*` var to `0` to disable that bucket.
- **Preview GC** — expired drafts are purged on write _and_ by a background
  goroutine every `PREVIEW_GC_INTERVAL`, so the `previews` table stays bounded
  even on an idle server.
- **Security headers** — every response carries
  `X-Content-Type-Options:
  nosniff`, `X-Frame-Options: DENY` and
  `Referrer-Policy: no-referrer`; JSON responses (and the captcha image proxy)
  are sent `Cache-Control: no-store`.
- **Metrics** — `GET /metrics` (unauthenticated) returns per-route request
  counters, a latency histogram, auth-failure counts and, when
  `ENRICH_OFF=true`, the Open Food Facts cache hit/miss counters as JSON.
- **Contract** — `api/openapi.yaml` is the versioned source of truth for the
  HTTP surface; the SPA consumes only that.

### Container image:

```sh
docker build -t meupreco .
docker run --rm -p 8080:8080 meupreco
```

TODO: implement docker-compose that pulls dockerhub image?

## Documentation

- `api/openapi.yaml` — versioned API contract (the SPA consumes only this).
- Local interactive API documentation is available at `/docs/` (or `/`) when the
  server is running. The raw contract is available at `/openapi.yaml` and
  `/openapi.json`.
- Web tooling lives under `web/`; `web/node_modules/` is local-only and must not
  be committed. From the repository root, run:

  ```sh
  make openapi-check      # validate the contract
  make openapi-generate   # regenerate api/openapi.json
  make openapi-diff       # fail if generated JSON is stale
  make bundle             # build dist/web for static hosting
  make static             # refresh embedded Go documentation assets
  ```

  `api/openapi.yaml` is the only hand-maintained contract. The JSON file,
  `dist/web/`, and `internal/httpapi/static/` are generated outputs. Run
  `make static` after changing the contract or documentation assets so the
  embedded local documentation stays current.
- GitHub Pages is deployed by `.github/workflows/pages.yml` from the same
  `dist/web/` bundle. The workflow validates the contract, generates JSON,
  builds the static bundle, and publishes both the interactive documentation and
  raw YAML/JSON specifications.

## Inspiration

- [Meu Guia de Compras](https://www.meuguiadecompras.com), but self hosted!

## Non-goals

- **Frontend** - this is server-only
- **multi-state** - For now, only São Paulo receipts will be implemented; other
  states return `501 unsupported_uf` (the registry/UF capability matrix is the
  seam for adding them).
