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
    - [Container image](#container-image)
      - [Building the image locally](#building-the-image-locally)
      - [Secrets and environment](#secrets-and-environment)
      - [Migrations against an external PostgreSQL instance](#migrations-against-an-external-postgresql-instance)
      - [Running the API with `docker run`](#running-the-api-with-docker-run)
      - [Running the complete local stack with Docker Compose](#running-the-complete-local-stack-with-docker-compose)
      - [Using a published image](#using-a-published-image)
      - [Checking health endpoints](#checking-health-endpoints)
      - [Viewing logs and stopping the stack](#viewing-logs-and-stopping-the-stack)
      - [Upgrading the image and applying pending migrations](#upgrading-the-image-and-applying-pending-migrations)
      - [External PostgreSQL database](#external-postgresql-database)
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

- [ ] Docker image
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
make hooks-install          # enable the repository pre-push validations

# Database: migrate up/down against POSTGRES_* + DATABASE_NAME
export POSTGRES_USERNAME='dev' POSTGRES_PASSWORD='dev' POSTGRES_HOST='localhost' DATABASE_NAME='meupreco'
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

> [!NOTE]
> The pre-push hook formats Go code, regenerates the OpenAPI documentation,
> updates the embedded documentation, and runs the Go vet and race-enabled test
> suite. If it changes tracked files, it stops so the changes can be reviewed
> and committed before pushing.

### env

The server expects some env. vars to be set.

| Var                   | Required | Default          | Accepted values                                                        |
| --------------------- | -------- | ---------------- | ---------------------------------------------------------------------- |
| PORT                  | no       | 8080             | 1–65535                                                                |
| LOG_LEVEL             | no       | info             | `debug` \| `info` \| `warn` \| `error`                                 |
| LOG_FORMAT            | no       | pretty           | `pretty` \| `json`                                                     |
| CORS_ORIGINS          | no       | \*               | comma-separated origins, or `*`                                        |
| SEFAZ_FETCH_RPS       | no       | 1                | integer ≥ 1                                                            |
| SEFAZ_MAX_RETRIES     | no       | 3                | integer ≥ 1 (fetch attempts per receipt)                               |
| SEFAZ_TIMEOUT         | no       | 30s              | Go duration (per portal request)                                       |
| SEFAZ_BACKOFF_BASE    | no       | 200ms            | Go duration ≥ 0 (start of the exponential retry delay)                 |
| SEFAZ_BACKOFF_CAP     | no       | 2s               | Go duration ≥ `SEFAZ_BACKOFF_BASE` (cap per retry delay)               |
| SEFAZ_BASE_URL        | no       | SP prod          | https URL (overrides the SP QR consultation endpoint for test proxies) |
| PREVIEWS_TTL          | no       | 15m              | Go duration > 0 (pending draft lifetime)                               |
| PREVIEW_GC_INTERVAL   | no       | 5m               | Go duration > 0 (background sweep period)                              |
| RATE_LIMIT_IP_RPS     | no       | 5                | float ≥ 0 (0 disables the per-IP bucket)                               |
| RATE_LIMIT_IP_BURST   | no       | 300              | integer ≥ 0 (per-IP burst)                                             |
| RATE_LIMIT_USER_RPS   | no       | 20               | float ≥ 0 (0 disables the per-user bucket)                             |
| RATE_LIMIT_USER_BURST | no       | 1000             | integer ≥ 0 (per-user burst)                                           |
| ENRICH_OFF            | no       | false            | `true`/`false` (best-effort Open Food Facts enrichment of `gtin_new`)  |
| POSTGRES_USERNAME     | yes      | —                | postgres login (builds the DSN)                                        |
| POSTGRES_PASSWORD     | yes      | —                | postgres password (builds the DSN; never logged/committed)             |
| DATABASE_NAME         | yes      | —                | database name, e.g. `meupreco`                                         |
| POSTGRES_HOST         | yes      | —                | postgres host (no default; see the note below)                         |
| POSTGRES_PORT         | no       | 5432             | postgres port (1–65535)                                                |
| JWT_ACCESS_TTL        | no       | 15m              | Go duration (e.g. `15m`)                                               |
| JWT_REFRESH_TTL       | no       | 720h             | Go duration (e.g. `720h`)                                              |
| JWT_SECRET            | yes      | —                | ≥ 32 chars; signs access + refresh tokens                              |
| ADMIN_EMAIL           | yes      | —                | seeds the first admin (role `admin`) at boot when `users` is empty     |
| ADMIN_PASSWORD        | yes      | —                | seeds the first admin password (bcrypt-hashed at boot)                 |
| ADMIN_NAME            | no       | email local part | seeds the first admin name (unique)                                    |

The DSN is built as `postgres://USER:PASS@HOST:PORT/DATABASE?sslmode=disable`.
Missing DB vars (or a too-short `JWT_SECRET`, or a missing `ADMIN_EMAIL`/
`ADMIN_PASSWORD`) abort startup with an explicit error. Secrets (DB password,
`JWT_SECRET`, `ADMIN_PASSWORD`) are only ever set via the environment — never
committed.

> [!IMPORTANT]
> `POSTGRES_HOST` is **required and has no default**. `localhost` means _the
> machine running the process_: the host for a native `make run`, but the
> container itself when the API runs in Docker — where it fails with a
> misleading `dial tcp 127.0.0.1:5432: connect: connection refused`. Set it
> explicitly to whatever the process must reach:
>
> | Where the process runs                                     | `POSTGRES_HOST`                                                                                                                     |
> | ---------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
> | native (`make run`)                                        | `localhost`                                                                                                                         |
> | container in the same Docker network as the database       | the database service name (e.g. `postgres`)                                                                                         |
> | container reaching a database published on the Docker host | the Docker bridge gateway, e.g. `172.17.0.1` (`docker network inspect bridge --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}'`) |
> | container with `--network host`                            | `localhost` (the host namespace is shared)                                                                                          |

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

### Docker

The image ships the API server and the migration tool, runs as a non-root user,
and has no Go toolchain inside. PostgreSQL is **not** part of the image: it
stays an external service (managed instance or your own container).

#### Building the image locally

```sh
docker build -t meupreco:local .
```

#### Secrets and environment

Required environment variables are supplied via an env file (`--env-file`) or
inline (`-e`). The server never reads a `.env` file at runtime — the binary only
sees what is passed to it.

```sh
# Build an env file from the example, then fill real values
cp .env.docker.example .env
# edit .env with your secrets, then:
docker run --rm --env-file .env meupreco:local migrate up
```

Do **not** commit the `.env` file that contains real credentials.

#### Migrations against an external PostgreSQL instance

> [!NOTE]
> `docker run` does not create PostgreSQL or apply migrations automatically. Use
> the `migrate` subcommand to run migrations idempotently against an existing
> database:

```sh
docker run --rm \
  -e POSTGRES_HOST=your-postgres-host \
  -e POSTGRES_PORT=5432 \
  -e POSTGRES_USERNAME=postgres \
  -e POSTGRES_PASSWORD=secretpassword \
  -e DATABASE_NAME=meupreco \
  -e JWT_SECRET=your-jwt-secret \
  -e ADMIN_EMAIL=admin@example.com \
  -e ADMIN_PASSWORD=strong-password \
  meupreco:local migrate up
```

The migration tool is included in the image; it connects to the provided
PostgreSQL instance and applies embedded migrations.

#### Running the API with `docker run`

```sh
docker run --rm \
  -p 8080:8080 \
  --env-file .env \
  meupreco:local
```

- `POSTGRES_HOST` is mandatory: `localhost` inside the container refers to the
  container itself, not your database. Set it to a reachable host from the
  container (e.g., the Docker bridge gateway, a managed DB hostname, or the name
  of a Compose service).
- `PORT` (default `8080`) is the port the API listens on. Publish the same port
  (`-p 8080:8080`) or override both (`-e PORT=9000 -p 9000:9000`).
- The image never reads a `.env` file; pass variables with `--env-file` or `-e`.

#### Running the complete local stack with Docker Compose

```sh
cp .env.docker.example .env
# edit .env with real values
docker compose --env-file .env up -d --build
```

This starts PostgreSQL, applies migrations, and starts the API. The API can
resolve PostgreSQL via the service name `postgres` — no `localhost` needed.

Re-running the stack does not reapply destructive migrations; the named
PostgreSQL data volume persists.

```sh
# Stop the stack, keep data
docker compose down

# Remove data volumes intentionally
docker compose down -v
```

#### Using a published image

When `MEUPRECO_IMAGE` and `IMAGE_TAG` are set, the same Compose file runs a
published image without rebuilding locally:

```sh
docker compose --env-file .env up --build \
  -e MEUPRECO_IMAGE=your-registry/meupreco \
  -e IMAGE_TAG=v1.0.0
```

#### Checking health endpoints

- `GET /healthz` — liveness check. Returns `200` if the process is alive.
- `GET /readyz` — readiness check. Returns `200` only if the process is alive
  **and** PostgreSQL is reachable. Use `/healthz` as the container health check;
  `/readyz` is not suitable because it depends on the external database.

```sh
curl http://127.0.0.1:8080/healthz   # {"status":"ok"}
curl http://127.0.0.1:8080/readyz   # {"status":"ok"} or {"status":"not_ready"}
```

#### Viewing logs and stopping the stack

```sh
# Stream logs
docker compose logs -f

# Stop the stack
docker compose down
```

#### Upgrading the image and applying pending migrations

When upgrading to a new image version, apply migrations **before** accepting
traffic. Start with the migration command, then start the API:

```sh
docker run --rm --env-file .env meupreco:local migrate up
docker run --rm -p 8080:8080 --env-file .env meupreco:local
```

Or with Compose (recreating only the API service):

```sh
docker compose --env-file .env up --build --force-recreate app
```

#### External PostgreSQL database

To use an external or managed PostgreSQL instance (recommended for production),
override the database credentials and host:

```sh
docker run --rm \
  -p 8080:8080 \
  --env-file .env \
  -e POSTGRES_HOST=prod-db.example.com \
  -e POSTGRES_PORT=5432 \
  -e POSTGRES_USERNAME=meupreco_user \
  -e POSTGRES_PASSWORD=strong-password \
  -e DATABASE_NAME=meupreco \
  meupreco:local
```

**Important:** The production recommendation is an external or managed
PostgreSQL service. Running PostgreSQL inside the application image is
explicitly out of scope — it keeps data lifecycle explicit, avoids accidental
data loss, and leverages managed service features (backups, encryption,
monitoring).

The `docker run` command does not create PostgreSQL or automatically apply
migrations. This keeps data lifecycle and schema changes explicit and avoids
silently modifying a user-provided database.

## Documentation

> [!NOTE]
> The Swagger API docs can be found
> [here](https://pedromarquetti.github.io/meu-preco/)

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
