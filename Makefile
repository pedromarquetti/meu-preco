BIN := bin/server

# Load the gitignored .env for local dev; no-op when absent. Production/Docker
# injects environment directly, so the binary never reads .env itself.
ENV_LOAD := if [ -f .env ]; then set -a; . ./.env; set +a; fi;

.PHONY: run build test lint vet fmtcheck staticcheck tidy clean migrate-up migrate-down migrate-version

run:
	@$(ENV_LOAD) go run ./cmd/server

build:
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

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)

# Migrations build the DSN from POSTGRES_USERNAME/POSTGRES_PASSWORD/DATABASE_NAME.
migrate-up:
	@$(ENV_LOAD) go run ./cmd/migrate up

migrate-down:
	@$(ENV_LOAD) go run ./cmd/migrate down

migrate-version:
	@$(ENV_LOAD) go run ./cmd/migrate version
