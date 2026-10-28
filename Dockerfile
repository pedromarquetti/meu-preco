# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Build stage: both binaries (API server + migration tool) come from the same
# Go toolchain, so the runtime image never needs Go.
# ---------------------------------------------------------------------------
FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# ---------------------------------------------------------------------------
# Runtime stage: Alpine + CA certificates (the SEFAZ portal is HTTPS-only),
# running as an unprivileged user.
# ---------------------------------------------------------------------------
FROM alpine:3.22

# -h /app creates the working directory owned by the unprivileged user.
RUN apk add --no-cache ca-certificates \
    && adduser -D -u 10001 -h /app app

WORKDIR /app

COPY --from=build /out/server /usr/local/bin/server
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh

USER app

# PORT is the listener port of the API (1-65535). It is baked as the default
# and can be overridden at runtime with `docker run -e PORT=...`; the
# container healthcheck always follows the effective value.
ARG PORT=8080
ENV PORT=${PORT}
EXPOSE ${PORT}

# Liveness only: /readyz intentionally depends on PostgreSQL, so it must not
# decide the container health (the database may be temporarily unreachable).
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${PORT}/healthz" || exit 1

# `docker run meupreco` starts the API server; `docker run meupreco migrate up`
# runs the migrations with the same image and configuration.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["server"]
