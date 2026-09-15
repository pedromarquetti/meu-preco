package httpapi

import (
	"net/http"

	"meupreco/internal/httpx"
)

// healthz reports process liveness (always 200 while running).
func healthz(w http.ResponseWriter, r *http.Request) {
	httpx.Respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz reports readiness. Phase 1 flips this to a DB ping that returns 503
// when the database is unreachable.
func readyz(w http.ResponseWriter, r *http.Request) {
	httpx.Respond(w, http.StatusOK, map[string]string{"status": "ok"})
}
