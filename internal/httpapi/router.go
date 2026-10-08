// Package httpapi wires the chi router and HTTP handlers.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"meupreco/internal/config"
	"meupreco/internal/domain"
	"meupreco/internal/httpx"
	"meupreco/internal/middleware"
	"meupreco/internal/nfce"
)

// New builds the full HTTP handler from the validated configuration.
// stores drives /readyz (Ping) and auth endpoints.
func New(cfg config.Config, log *slog.Logger, stores domain.Stores) http.Handler {
	return newRouter(cfg, log, newNfceResolver(cfg, log), stores)
}

// newRouter assembles the router with the given nfce resolver (kept injectable
// for httptest tests).
func newRouter(cfg config.Config, log *slog.Logger, nfceSvc nfceResolver, stores domain.Stores) http.Handler {
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
	router.Get("/readyz", readyz(stores))

	authAPI := newAuthAPI(cfg, stores)

	router.Route("/api/v1", func(r chi.Router) {
		r.Route("/nfce", func(nr chi.Router) {
			nr.Post("/submit", handleNfceSubmit(nfceSvc))
			nr.NotFound(http.HandlerFunc(httpx.NotImplemented))
		})

		r.Route("/auth", func(ar chi.Router) {
			ar.Post("/register", authAPI.register)
			ar.Post("/login", authAPI.login)
			ar.Post("/refresh", authAPI.refreshHandler)
			ar.With(authAPI.mw.RequireAuth).Get("/me", authAPI.me)
			ar.NotFound(httpx.NotFound)
		})

		// The admin group exists and is gated (401 without
		// token, 403 for non-admin, 200 for admin).
		r.Route("/admin", func(ar chi.Router) {
			ar.Use(authAPI.mw.RequireRole("admin"))
			ar.Get("/", adminStub)
			ar.NotFound(httpx.NotFound)
		})

		for _, prefix := range []string{"/price-logs", "/products"} {
			r.Mount(prefix, http.HandlerFunc(httpx.NotImplemented))
		}
	})

	return router
}

// adminStub is the admin-gated placeholder; it answers 200 only when
// an admin token passed the RequireRole gate.
func adminStub(w http.ResponseWriter, _ *http.Request) {
	httpx.Respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// newNfceResolver wires a per-UF dispatch table from the nfce registry.
// Only probe-verified states (currently São Paulo) get a resolver.
// The remaining states are answered with 501 by the MultiResolver.
func newNfceResolver(cfg config.Config, log *slog.Logger) nfceResolver {
	byUF := make(map[string]*nfce.Resolver)
	for _, uf := range nfce.Registry() {
		if !uf.Supported {
			continue
		}

		baseURL := uf.QRBaseURL

		if uf.UF == "35" && cfg.SefazBaseURL != "" {
			// The SP base URL is overridable (SEFAZ_BASE_URL) so fixtures and
			// deployments can point it at an alternate endpoint.
			baseURL = cfg.SefazBaseURL
		}

		if baseURL == "" {
			baseURL = nfce.DefaultSPQRBaseURL
		}

		src, err := nfce.NewHTTPSource(nfce.HTTPSourceOptions{
			BaseURL: baseURL,
			RPS:     cfg.SEFAZFetchRPS,
			Logger:  log,
		})

		if err != nil {
			log.Error("httpapi: cannot build nfce source", "uf", uf.UF, "err", err)
			continue
		}

		// All supported states belong to the same "Consulta Resumida" page
		// family today, so one Parser serves them all. A state with its own
		// portal family gets its own Parser (and file) when added.
		byUF[uf.UF] = nfce.NewResolver(src, &nfce.HTMLParser{Logger: log})
	}

	if len(byUF) == 0 {
		return nil
	}

	return nfce.NewMultiResolver(byUF)
}
