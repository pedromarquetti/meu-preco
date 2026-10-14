BIN := bin/server

# Load the gitignored .env for local dev; no-op when absent. Production/Docker
# injects environment directly, so the binary never reads .env itself.
ENV_LOAD := if [ -f .env ]; then set -a; . ./.env; set +a; fi;

.PHONY: run build test lint vet fmtcheck staticcheck tidy clean-web clean-bin migrate-up migrate-down migrate-version openapi-json openapi-check

run: static
	@$(ENV_LOAD) go run ./cmd/server

build: static
	go build -o $(BIN) ./cmd/server

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
