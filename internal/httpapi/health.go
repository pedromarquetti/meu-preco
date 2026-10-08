package httpapi

import (
	"context"
	"errors"
	"net/http"

	"meupreco/internal/httpx"
)

// pinger is the slice of the store the readiness check uses.
type pinger interface {
	Ping(ctx context.Context) error
}

// healthz reports process liveness (always 200 while running).
func healthz(w http.ResponseWriter, r *http.Request) {
	httpx.Respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz reports readiness: the database must answer a ping. A missing or
// unreachable database yields 503 so load balancers stop routing traffic to
// this instance.
func readyz(db pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if db == nil {
			httpx.Respond(w, http.StatusServiceUnavailable,
				map[string]string{"status": "unavailable", "reason": "database not configured"})
			return
		}

		if err := db.Ping(r.Context()); err != nil && !errors.Is(err, context.Canceled) {
			httpx.Respond(w, http.StatusServiceUnavailable,
				map[string]string{"status": "unavailable", "reason": "database unreachable"})
			return
		}

		httpx.Respond(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
