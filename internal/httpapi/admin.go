package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"meupreco/internal/domain"
	"meupreco/internal/httpx"
	"meupreco/internal/service"
)

const maxAdminBody = 1 << 20 // 1 MiB is generous for a batch-assign body

// adminAPI serves the admin curation endpoints, all behind RequireRole("admin"):
//
//	GET    /api/v1/admin/users                       — list users
//	PATCH  /api/v1/admin/users/{id}/role             — promote/demote
//	GET    /api/v1/admin/products/needs-review       — review queue
//	POST   /api/v1/admin/products/batch-assign       — resolve queue rows
//	POST   /api/v1/admin/products/merge              — fold two products
//	PATCH  /api/v1/admin/products/{id}               — curate name/brand/unit
//	DELETE /api/v1/admin/products/{id}               — guarded delete (?purge)
//	POST   /api/v1/admin/supermarkets/merge          — fold two emitters by CNPJ
type adminAPI struct {
	svc *service.Service
}

func newAdminAPI(svc *service.Service) *adminAPI {
	return &adminAPI{svc: svc}
}

func (api *adminAPI) listUsers(w http.ResponseWriter, r *http.Request) {
	page, limit, ok := readPager(w, r)
	if !ok {
		return
	}
	res, err := api.svc.ListUsers(r.Context(), page, limit)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, res)
}

func (api *adminAPI) patchRole(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	callerID, ok := subjectID(w, r)
	if !ok {
		return
	}

	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", `body must be valid JSON: {"role":"user|admin"}`)
		return
	}
	if err := api.svc.UpdateUserRole(r.Context(), callerID, userID, body.Role); err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusNoContent, nil)
}

func (api *adminAPI) needsReview(w http.ResponseWriter, r *http.Request) {
	page, limit, ok := readPager(w, r)
	if !ok {
		return
	}
	res, err := api.svc.NeedsReviewQueue(r.Context(), page, limit)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, res)
}

func (api *adminAPI) batchAssign(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminBody)

	var body service.BatchAssignRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request",
			`body must be valid JSON: {"assignments":[{"id","action","product_id|name|brand|unit"}]}`)
		return
	}
	adminID, ok := subjectID(w, r)
	if !ok {
		return
	}
	res, err := api.svc.BatchAssign(r.Context(), adminID, body)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, res)
}

func (api *adminAPI) mergeProducts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromID int64 `json:"from_id"`
		ToID   int64 `json:"to_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", `body must be valid JSON: {"from_id","to_id"}`)
		return
	}
	res, err := api.svc.MergeProducts(r.Context(), body.FromID, body.ToID)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, res)
}

func (api *adminAPI) mergeSupermarkets(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromCNPJ string `json:"from_cnpj"`
		ToCNPJ   string `json:"to_cnpj"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", `body must be valid JSON: {"from_cnpj","to_cnpj"}`)
		return
	}
	res, err := api.svc.MergeSupermarkets(r.Context(), body.FromCNPJ, body.ToCNPJ)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, res)
}

func (api *adminAPI) patchProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var patch domain.ProductPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", `body must be valid JSON: {"name","brand","unit"}`)
		return
	}
	if patch.Name == nil && patch.Brand == nil && patch.Unit == nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request",
			"at least one of name, brand or unit must be provided")
		return
	}
	res, err := api.svc.UpdateProduct(r.Context(), id, patch)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, res)
}

func (api *adminAPI) deleteProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	purge := strings.EqualFold(r.URL.Query().Get("purge"), "true")
	res, err := api.svc.DeleteProduct(r.Context(), id, purge)
	if err != nil {
		respondServiceError(w, r, err)
		return
	}
	httpx.Respond(w, http.StatusOK, res)
}
