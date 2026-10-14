// Package httpapi wires the chi router and HTTP handlers.
package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meupreco/internal/auth"
	"meupreco/internal/config"
	"meupreco/internal/domain"
	"meupreco/internal/enrich"
	"meupreco/internal/httpx"
	"meupreco/internal/middleware"
	"meupreco/internal/nfce"
	"meupreco/internal/service"
)

// New builds the full HTTP handler from the validated configuration.
// stores drives /readyz (Ping) and auth endpoints.
func New(cfg config.Config, log *slog.Logger, stores domain.Stores) http.Handler {
	return newRouter(cfg, log, newNfceResolver(cfg, log), stores, nil)
}

// newRouter assembles the router with the given nfce resolver (kept injectable
// for httptest tests).
// TODO: handle PostgreSQL errors correctly: the server should at least log the error so the server admin can see what is going on (nfceAPI currently returns http 500 if there is a postgres err)
func newRouter(cfg config.Config, log *slog.Logger, nfceSvc nfceResolver, stores domain.Stores, captcha *service.CaptchaSupport) http.Handler {
	router := chi.NewRouter()

	router.Use(
		middleware.RequestID,
		middleware.Logging(log),
		middleware.PanicRecovery(log),
		middleware.CORS(cfg.CORSOrigins),
		middleware.SecurityHeaders,
	)

	router.NotFound(httpx.NotFound)
	router.MethodNotAllowed(httpx.MethodNotAllowed)

	// A shared Open Food Facts lookup client backs both the opt-in gtin_new
	// enrichment and the OFF cache-hit stats surfaced on /metrics. Nil when
	// enrichment is disabled.
	var offClient *enrich.OFFClient
	if cfg.EnableOFFEnrich {
		offClient = enrich.NewOFFClient()
	}

	// Health checks and metrics stay unprefixed; everything else lives under
	// /api/v1/.
	router.Get("/healthz", healthz)
	router.Get("/readyz", readyz(stores))
	router.Get("/metrics", metricsHandler(offClient))

	// Redirect root to /docs/
	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusFound)
	})

	// Serve documentation from the static bundle.
	router.Mount("/docs/", http.StripPrefix("/docs/", http.FileServer(http.Dir("dist"))))
	router.Get("/openapi.yaml", http.FileServer(http.Dir("dist")).ServeHTTP)
	router.Get("/openapi.json", http.FileServer(http.Dir("dist")).ServeHTTP)

	authAPI := newAuthAPI(cfg, stores)

	router.Route("/api/v1", func(r chi.Router) {
		// A global per-IP token bucket throttles every API request; a
		// per-user bucket is layered in below on the authenticated groups.
		// Zero/negative configuration values disable the buckets (tests).
		r.Use(middleware.KeyedRateLimit(cfg.RateLimitIPRPS, cfg.RateLimitIPBurst, middleware.IPKey))
		userRL := middleware.KeyedRateLimit(cfg.RateLimitUserRPS, cfg.RateLimitUserBurst, userRateKey)

		if captcha == nil {
			captcha = newCaptchaSupport(cfg, log)
		}

		// One service instance drives every group: ingestion, reads and admin
		// curation all share the same stores + resolver + transaction boundary.
		svc := service.New(stores, nfceSvc, log, captcha)
		if cfg.PreviewTTL > 0 {
			svc.PreviewTTL = cfg.PreviewTTL
		}
		if offClient != nil {
			svc.Enricher = offClient
		}

		nfceAPI := newNfceAPI(svc, log)
		productsAPI := newProductsAPI(svc)
		supermarketsAPI := newSupermarketsAPI(svc)
		adminAPI := newAdminAPI(svc)

		// Two-phase receipt ingestion + receipt lifecycle, authenticated:
		// POST /nfce/new      — draft: fetch + classify, persist only a plan
		// POST /nfce/submit   — commit the confirmed plan atomically
		// POST /nfce/delete   — delete a receipt (owner OR admin)
		// POST /nfce/captcha  — solve a portal challenge into a draft
		r.Route("/nfce", func(nr chi.Router) {
			nr.Use(authAPI.mw.RequireAuth, userRL)
			nr.Post("/new", nfceAPI.draft)
			nr.Post("/submit", nfceAPI.confirm)
			nr.Post("/delete", nfceAPI.delete)
			nr.Get("/captcha", nfceAPI.captcha.state)
			nr.Post("/captcha", nfceAPI.captcha.solve)
			nr.Get("/captcha/image/{challenge_id}", nfceAPI.captcha.image)
			nr.NotFound(http.HandlerFunc(httpx.NotImplemented))
		})

		r.Route("/auth", func(ar chi.Router) {
			ar.Post("/register", authAPI.register)
			// TODO: check if a logout endpoint should be implemented here
			ar.Post("/login", authAPI.login)
			ar.Post("/refresh", authAPI.refreshHandler)
			ar.With(authAPI.mw.RequireAuth, userRL).Get("/me", authAPI.me)
			ar.NotFound(httpx.NotFound)
		})

		// Shared product/supermarket reads, authenticated. Public reads never
		// surface needs_review rows (the store filters them out).
		r.Route("/products", func(pr chi.Router) {
			pr.Use(authAPI.mw.RequireAuth, userRL)
			pr.Get("/", productsAPI.search)
			pr.Get("/{id}", productsAPI.get)
			pr.Get("/{id}/history", productsAPI.history)
			pr.NotFound(httpx.NotFound)
		})

		r.Route("/supermarkets", func(sr chi.Router) {
			sr.Use(authAPI.mw.RequireAuth, userRL)
			sr.Get("/", supermarketsAPI.list)
			sr.Get("/{id}/products", supermarketsAPI.products)
			sr.With(authAPI.mw.RequireRole("admin")).Delete("/{id}", supermarketsAPI.remove)
			sr.NotFound(httpx.NotFound)
		})

		// Own-profile alias: /api/v1/users/me mirrors /api/v1/auth/me.
		r.Route("/users", func(ur chi.Router) {
			ur.Use(authAPI.mw.RequireAuth, userRL)
			ur.Get("/me", authAPI.me)
			ur.NotFound(httpx.NotFound)
		})

		// Admin curation, everything gated by RequireRole("admin").
		r.Route("/admin", func(ar chi.Router) {
			ar.Use(authAPI.mw.RequireRole("admin"), userRL)
			ar.Get("/users", adminAPI.listUsers)
			ar.Patch("/users/{id}/role", adminAPI.patchRole)
			ar.Get("/products/needs-review", adminAPI.needsReview)
			ar.Post("/products/batch-assign", adminAPI.batchAssign)
			ar.Post("/products/merge", adminAPI.mergeProducts)
			ar.Patch("/products/{id}", adminAPI.patchProduct)
			ar.Delete("/products/{id}", adminAPI.deleteProduct)
			ar.Post("/supermarkets/merge", adminAPI.mergeSupermarkets)
			ar.NotFound(httpx.NotFound)
		})
	})

	return router
}

// userRateKey keys the per-user token bucket by the authenticated principal;
// an unauthenticated request (should not reach these routes) falls back to the
// client IP.
func userRateKey(r *http.Request) string {
	if p := auth.Principal(r.Context()); p != nil {
		if id, err := auth.SubjectID(p); err == nil {
			return "user:" + strconv.FormatInt(id, 10)
		}
	}
	return middleware.IPKey(r)
}

// newCaptchaSupport wires the captcha flow: an in-memory challenge
// registry shared with the portal source (which registers challenges when the
// portal demands one) plus a solver that replays typed answers into the
// challenge's captured session. The MaxAttempts here echoes the store default
// so solve-time and challenge-mint time agree.
func newCaptchaSupport(cfg config.Config, log *slog.Logger) *service.CaptchaSupport {
	return &service.CaptchaSupport{
		Store:       nfce.NewChallengeStore(),
		Solver:      nfce.NewHTTPChallengeSolver(nfce.HTTPChallengeSolverOptions{RPS: cfg.SEFAZFetchRPS, Timeout: cfg.SEFAZTimeout, Logger: log}),
		MaxAttempts: 5,
	}
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
			BaseURL:     baseURL,
			RPS:         cfg.SEFAZFetchRPS,
			MaxRetries:  cfg.SEFAZMaxRetries,
			Timeout:     cfg.SEFAZTimeout,
			BackoffBase: cfg.SEFAZBackoffBase,
			BackoffCap:  cfg.SEFAZBackoffCap,
			Logger:      log,
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
