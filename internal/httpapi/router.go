// Package httpapi wires the chi router and HTTP handlers.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meupreco/internal/config"
	"meupreco/internal/httpx"
	"meupreco/internal/middleware"
	"meupreco/internal/nfce"
)

// New builds the full HTTP handler from the validated configuration.
// Endpoint groups that belong to later phases are mounted as 501 stubs so the
// route boundary is visible from day one and handlers mount in place later.
func New(cfg config.Config, log *slog.Logger) http.Handler {
	return newRouter(cfg, log, newNfceResolver(cfg, log))
}

// newRouter assembles the router with the given nfce resolver (kept injectable
// for httptest tests).
func newRouter(cfg config.Config, log *slog.Logger, nfceSvc nfceResolver) http.Handler {
	router := chi.NewRouter()

	router.Use(
		middleware.RequestID,
		middleware.Logging(log),
		middleware.PanicRecovery(log),
		middleware.CORS(cfg.CORSOrigins),
	)

	router.NotFound(httpx.NotFound)
	router.MethodNotAllowed(httpx.MethodNotAllowed)

	// Health checks stay unprefixed; everything else lives under /api/v1/.
	router.Get("/healthz", healthz)
	router.Get("/readyz", readyz)

	router.Route("/api/v1", func(r chi.Router) {
		r.Route("/nfce", func(nr chi.Router) {
			nr.Post("/submit", handleNfceSubmit(nfceSvc))
			nr.NotFound(http.HandlerFunc(httpx.NotImplemented))
		})

		for _, prefix := range []string{"/auth", "/price-logs", "/products", "/admin"} {
			r.Mount(prefix, http.HandlerFunc(httpx.NotImplemented))
		}
	})

	return router
}

// newNfceResolver wires the São Paulo source + HTML parser into a resolver.
func newNfceResolver(cfg config.Config, log *slog.Logger) nfceResolver {
	src, err := nfce.NewSPSource(nfce.SPSourceOptions{
		BaseURL:    cfg.SefazBaseURL,
		RPS:        cfg.SEFAZFetchRPS,
		HTTPClient: nil,
		Logger:     log,
	})
	if err != nil {
		// NewSPSource can only fail on a malformed cookie jar (never with the
		// defaults); a resolver that cannot resolve is a nil stub.
		return nil
	}
	return nfce.NewResolver(src, &nfce.HTMLParser{Logger: log})
}
