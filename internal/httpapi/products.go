package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"meupreco/internal/domain"
	"meupreco/internal/httpx"
	"meupreco/internal/service"
)

// productsAPI serves the shared product reads behind RequireAuth:
//
//	GET /api/v1/products              — paginated pg_trgm search
//	GET /api/v1/products/{id}         — one product
//	GET /api/v1/products/{id}/history — price curve + per-unit aggregate
type productsAPI struct {
	svc *service.Service
}

func newProductsAPI(svc *service.Service) *productsAPI {
	return &productsAPI{svc: svc}
}

// search implements GET /api/v1/products.
func (api *productsAPI) search(w http.ResponseWriter, r *http.Request) {
	page, limit, ok := readPager(w, r)
	if !ok {
		return
	}
	res, err := api.svc.SearchProducts(r.Context(), domain.ProductFilter{
		Query: r.URL.Query().Get("q"),
		GTIN:  r.URL.Query().Get("gtin"),
		Unit:  r.URL.Query().Get("unit"),
		Brand: r.URL.Query().Get("brand"),
	}, page, limit)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, res)
}

func (api *productsAPI) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	p, err := api.svc.GetProduct(r.Context(), id)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, p)
}

func (api *productsAPI) history(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	from, to, ok := parseWindow(w, r)
	if !ok {
		return
	}
	h, err := api.svc.ProductHistory(r.Context(), id, from, to)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, h)
}
