BIN := bin/server

# Load the gitignored .env for local dev; no-op when absent. Production/Docker
# injects environment directly, so the binary never reads .env itself.
ENV_LOAD := if [ -f .env ]; then set -a; . ./.env; set +a; fi;

.PHONY: run build build-check ci pre-push test lint vet fmtcheck fmtfix staticcheck tidy clean-web clean-bin hooks-install migrate-up migrate-down migrate-version openapi-generate openapi-check openapi-diff bundle static

run: static
	@$(ENV_LOAD) go run ./cmd/server

build: static
	go build -o $(BIN) ./cmd/server

build-check:
	go build ./...

ci: fmtcheck vet build-check test

pre-push:
	gofmt -s -w .
	@command -v npm >/dev/null 2>&1 || { echo "pre-push: npm is required to update OpenAPI documentation" >&2; exit 1; }
	npm ci --prefix web
	npm run generate --prefix web
	make static
	@if ! git diff --quiet; then \
		echo "pre-push: formatting or generated documentation changed." >&2; \
		echo "Review and commit the changes before pushing." >&2; \
		git diff --stat; \
		exit 1; \
	fi
	$(MAKE) ci

test:
	go test ./... -race -count=1

lint: 	fmtcheck vet staticcheck

fmtfix:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "gofmt issues in: $$files, fixing with gofmt -s -w..."; \
		gofmt -s -w "$$files"; \
		exit 1; \
	else \
		echo "gofmt: ok"; \
	fi

fmtcheck:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "gofmt issues in:"; \
		echo "$$files"; \
		exit 1; \
	else \
		echo "gofmt: ok"; \
	fi

vet:
	go vet ./...

staticcheck:
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	else \
		echo "staticcheck not installed; skipping (go install honnef.co/go/tools/cmd/staticcheck@latest)"; \
	fi

openapi-generate:
	cd web && npm run generate

openapi-check:
	cd web && npm run check

openapi-diff:
	cd web && npm run check-diff

bundle:
	cd web && node scripts/build-bundle.mjs

static: bundle
	rm -rf internal/httpapi/static
	cp -r dist internal/httpapi/static

hooks-install:
	git config core.hooksPath .githooks

clean:	clean-web clean-bin

clean-web:
	rm -rf dist

clean-bin:
	rm -rf $(BIN)

tidy:
	go mod tidy

# Migrations build the DSN from POSTGRES_USERNAME/POSTGRES_PASSWORD/DATABASE_NAME.
migrate-up:
	@$(ENV_LOAD) go run ./cmd/migrate up

migrate-down:
	@$(ENV_LOAD) go run ./cmd/migrate down

migrate-version:
	@$(ENV_LOAD) go run ./cmd/migrate version

# Import a gzipped Open Food Facts products dump (789/790 GTINs only, idempotent).
seed-off:
	@$(ENV_LOAD) go run ./cmd/seed-off $(ARGS)
