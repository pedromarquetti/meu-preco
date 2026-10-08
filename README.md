# MeuPreço

<!--toc:start-->

- [MeuPreço](#meupreço)
  - [Goals](#goals)
  - [How it works](#how-it-works)
  - [Stack](#stack)
  - [Current status](#current-status)
  - [Getting started](#getting-started)
    - [Run with make](#run-with-make)
    - [env](#env)
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
- **Multi-UF by design** — Other states will be implemented in the future, for
  now, only receipts from _São Paulo_ are supported.

## How it works

```
QR-code payload / access_key
    │  POST /api/v1/nfce/submit   (qr_payload preferred, key fallback)
    ▼
registry dispatch ──► 501 unsupported_uf  (non-SP states today)
    │
    ▼
HTTPSource → SEFAZ portal (throttled, cookie jar, captcha-aware)
    │  ← exact scanned payload replayed for v2/offline variants
    ▼
parse DANFE ("Consulta Resumida" family — products, prices, supermarket, totals)
    │
    ▼
identity resolution (GTIN / commodity key) + itemized preview
    │        POST /api/v1/price-logs  (confirm)
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

# Database (Phase 2): migrate up/down against POSTGRES_* + DATABASE_NAME
export POSTGRES_USERNAME='dev' POSTGRES_PASSWORD='dev' DATABASE_NAME='meupreco'
make migrate-up
make migrate-down
make migrate-version

# Postgres integration tests use their own database ($MEUPRECO_TEST_DATABASE_URL)
export MEUPRECO_TEST_DATABASE_URL='postgres://dev:dev@localhost:5432/meupreco_test?sslmode=disable'
go test ./internal/store/...
```

### env

The server expects some env. vars to be set.

| Var               | Required | Default   | Accepted values                                                        |
| ----------------- | -------- | --------- | ---------------------------------------------------------------------- |
| PORT              | no       | 8080      | 1–65535                                                                |
| LOG_LEVEL         | no       | info      | `debug` \| `info` \| `warn` \| `error`                                 |
| LOG_FORMAT        | no       | pretty    | `pretty` \| `json`                                                     |
| CORS_ORIGINS      | no       | \*        | comma-separated origins, or `*`                                        |
| SEFAZ_FETCH_RPS   | no       | 1         | integer ≥ 1                                                            |
| SEFAZ_BASE_URL    | no       | SP prod   | https URL (overrides the SP QR consultation endpoint for test proxies) |
| POSTGRES_USERNAME | yes      | —         | postgres login (builds the DSN)                                        |
| POSTGRES_PASSWORD | yes      | —         | postgres password (builds the DSN; never logged/committed)             |
| DATABASE_NAME     | yes      | —         | database name, e.g. `meupreco`                                         |
| POSTGRES_HOST     | no       | localhost | postgres host                                                          |
| POSTGRES_PORT     | no       | 5432      | postgres port (1–65535)                                                |
| JWT_ACCESS_TTL    | no       | 15m       | Go duration (e.g. `15m`)                                               |
| JWT_REFRESH_TTL   | no       | 720h      | Go duration (e.g. `720h`)                                              |
| JWT_SECRET        | Phase 3  | —         | ≥ 32 chars (required when auth lands)                                  |

The DSN is built as `postgres://USER:PASS@HOST:PORT/DATABASE?sslmode=disable`.
Missing DB vars abort startup with an explicit error. Secrets (DB password,
`JWT_SECRET`) are only ever set via the environment — never committed.

`make run` and `make migrate-*` automatically load `./.env` if present
(KEY=VALUE shell syntax; copy `.env.example` to `.env` and fill in real
values). Without `.env`, vars must be exported in the shell. In Docker the
platform injects environment directly (`env_file`/`-e`) — the compiled binary
never reads `.env` itself.

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

## Inspiration

- [Meu Guia de Compras](https://www.meuguiadecompras.com), but self hosted!

## Non-goals

- **Frontend** - this is server-only
- **multi-state** - For now, only São Paulo receipts will be implemented, other
  states return 500 HTTP status
