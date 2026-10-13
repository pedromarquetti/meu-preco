package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"meupreco/internal/httpx"
	"meupreco/internal/service"
)

// supermarketsAPI serves the supermarket reads behind RequireAuth, plus the
// admin-guarded delete:
//
//	GET    /api/v1/supermarkets           — the emitters the user has logged
//	GET    /api/v1/supermarkets/{id}/products — products + latest price per store
//	DELETE /api/v1/supermarkets/{id}      — admin only, guarded by receipts
type supermarketsAPI struct {
	svc *service.Service
}

func newSupermarketsAPI(svc *service.Service) *supermarketsAPI {
	return &supermarketsAPI{svc: svc}
}

func (api *supermarketsAPI) list(w http.ResponseWriter, r *http.Request) {
	page, limit, ok := readPager(w, r)
	if !ok {
		return
	}
	res, err := api.svc.ListSupermarkets(r.Context(), page, limit)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, res)
}

func (api *supermarketsAPI) products(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	page, limit, ok := readPager(w, r)
	if !ok {
		return
	}
	res, err := api.svc.SupermarketProducts(r.Context(), id, page, limit)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, res)
}

func (api *supermarketsAPI) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if err := api.svc.DeleteSupermarket(r.Context(), id); err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusNoContent, nil)
}
