// Package httpapi wires the chi router and HTTP handlers.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meupreco/internal/config"
	"meupreco/internal/httpx"
	"meupreco/internal/middleware"
)

// New builds the full HTTP handler from the validated configuration.
// Endpoint groups that belong to later phases are mounted as 501 stubs so the
// route boundary is visible from day one and handlers mount in place later.
func New(cfg config.Config, log *slog.Logger) http.Handler {
	router := chi.NewRouter()

	router.Use(
		middleware.RequestID,
		middleware.Logging(log),
		middleware.PanicRecovery(log),
		middleware.CORS(cfg.CORSOrigins),
	)

	router.NotFound(httpx.NotFound)
	router.MethodNotAllowed(httpx.MethodNotAllowed)

	router.Get("/healthz", healthz)
	router.Get("/readyz", readyz)

	for _, prefix := range []string{"/auth", "/nfce", "/price-logs", "/products", "/admin"} {
		router.Mount(prefix, http.HandlerFunc(httpx.NotImplemented))
	}

	return router
}
