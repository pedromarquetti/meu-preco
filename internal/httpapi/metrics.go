package httpapi

import (
	"net/http"

	"meupreco/internal/enrich"
	"meupreco/internal/httpx"
	"meupreco/internal/middleware"
)

// metricsHandler serves GET /metrics: the per-route/latency/auth-failure
// request report plus, when Open Food Facts enrichment is enabled, its cache
// hit/miss counters. It is intentionally unauthenticated (scrapeable) but
// carries no receipt data.
func metricsHandler(off *enrich.OFFClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{
			"requests": middleware.Report(),
		}
		if off != nil {
			out["off_cache"] = off.CacheStats()
		}
		httpx.Respond(w, http.StatusOK, out)
	}
}
