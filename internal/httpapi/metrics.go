package httpapi

import (
	"net/http"

	"meupreco/internal/enrich"
	"meupreco/internal/httpx"
	"meupreco/internal/middleware"
	"meupreco/internal/nfce"
)

// metricsHandler serves GET /api/v1/admin/metrics: the per-route/latency/
// auth-failure request report, the Open Food Facts cache counters when
// enrichment is enabled, and the process-local fiscal-portal outcome counters.
// It is registered behind the admin role middleware and carries no receipt
// data, cookie values, or QR payloads.
func metricsHandler(off *enrich.OFFClient, outcomes *nfce.PortalOutcomes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{
			"requests": middleware.Report(),
		}
		if off != nil {
			out["off_cache"] = off.CacheStats()
		}
		if outcomes != nil {
			out["portal_outcomes"] = map[string]any{
				"totals":         outcomes.Totals(),
				"block_rate_pct": outcomes.BlockRate(),
				"hosts":          outcomes.Snapshot(),
			}
		}
		httpx.Respond(w, http.StatusOK, out)
	}
}
