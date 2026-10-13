package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"meupreco/internal/auth"
	"meupreco/internal/domain"
	"meupreco/internal/httpx"
	"meupreco/internal/nfce"
	"meupreco/internal/service"
)

const maxNfceBody = 1 << 16 // 64 KiB is plenty for a new/submit body

// nfceResolver is the slice of the nfce pipeline the ingestion handlers need.
// Keeping it an interface lets httptest tests stub the whole portal layer.
type nfceResolver interface {
	Resolve(ctx context.Context, key nfce.AccessKey, payload string) (*domain.Nfce, error)
}

// nfceAPI serves the ingestion endpoints, all behind RequireAuth:
//
//	POST /api/v1/nfce/new      — draft: fetch + classify, persist only a plan
//	POST /api/v1/nfce/submit   — commit: apply the confirmed plan atomically
//	POST /api/v1/nfce/delete   — delete a receipt (owner OR admin)
//
// The fetch is the draft step, and /nfce/submit is strictly the confirmation.
// captcha may be nil to disable the endpoints.
type nfceAPI struct {
	svc     *service.Service
	captcha *captchaAPI
}

func newNfceAPI(svc *service.Service, log *slog.Logger) *nfceAPI {
	return &nfceAPI{
		svc:     svc,
		captcha: newCaptchaAPI(svc, log),
	}
}

// draft implements POST /api/v1/nfce/new: normalize + validate the chave (or
// the QR payload that embeds it), fetch the DANFE from the issuing state's
// portal, run every line through the identity classifier and store the plan as
// a pending previews row. Nothing is written to the shared tables before a
// submit confirms.
//
// Idempotency is the receipt's access key: re-drafting the same receipt while
// a pending preview exists replays that plan (200) without a SEFAZ refetch; an
// expired draft is purged and re-planned. This endpoint never 409s — the
// strict already-committed 409 lives on /nfce/submit (same preview_id).
func (api *nfceAPI) draft(writer http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(writer, req.Body, maxNfceBody)

	var body struct {
		AccessKey string `json:"access_key"`
		QRPayload string `json:"qr_payload"`
	}

	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		httpx.Error(writer, req, http.StatusBadRequest, "invalid_request",
			`body must be valid JSON: {"access_key","qr_payload"}`)
		return
	}

	userID, ok := subjectID(writer, req)
	if !ok {
		return
	}

	res, err := api.svc.ReportPreview(req.Context(), service.PreviewRequest{
		UserID:    userID,
		AccessKey: body.AccessKey,
		QRPayload: body.QRPayload,
	})
	if err != nil {
		api.respondError(writer, req, err)
		return
	}

	httpx.Respond(writer, http.StatusOK, res)
}

// confirm implements POST /api/v1/nfce/submit: confirm the plan bound to a
// preview and apply it atomically (or, with confirm=false, report what a
// commit would do).
//
// 409 already_committed fires only when the SAME preview_id is confirmed
// twice by the same user (a wire-retry storm). Cross-user re-submits and
// already-ingested receipts are the silent dedup path: 200 with written:0 and
// the existing nfce_id, because nfces.access_key is unique.
func (api *nfceAPI) confirm(writer http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(writer, req.Body, maxNfceBody)

	var body struct {
		PreviewID   int64                `json:"preview_id"`
		Confirm     bool                 `json:"confirm"`
		Resolutions []service.Resolution `json:"resolutions"`
	}

	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		// TODO: make this more verbose: a failing req should return a explanation for the error
		httpx.Error(writer, req, http.StatusBadRequest, "invalid_request",
			`body must be valid JSON: {"preview_id","confirm","resolutions"}`)
		return
	}

	userID, ok := subjectID(writer, req)
	if !ok {
		return
	}

	res, err := api.svc.CommitPreview(req.Context(), service.CommitRequest{
		UserID:      userID,
		PreviewID:   body.PreviewID,
		Confirm:     body.Confirm,
		Resolutions: body.Resolutions,
	})

	if err != nil {
		api.respondError(writer, req, err)
		return
	}

	httpx.Respond(writer, http.StatusOK, res)
}

// delete implements POST /api/v1/nfce/delete: deletes the receipt plus its
// price_logs and preview drafts. Guarded by ownership (the caller who committed
// the receipt) OR an admin caller.
func (api *nfceAPI) delete(writer http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(writer, req.Body, maxNfceBody)

	var body service.DeleteReceiptRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		httpx.Error(writer, req, http.StatusBadRequest, "invalid_request",
			`body must be valid JSON: {"access_key"}`)
		return
	}

	userID, ok := subjectID(writer, req)
	if !ok {
		return
	}
	claims := auth.Principal(req.Context())
	callerIsAdmin := claims != nil && claims.Role == service.UserRoleAdmin

	res, err := api.svc.DeleteReceipt(req.Context(), userID, body, callerIsAdmin)
	if err != nil {
		api.respondError(writer, req, err)
		return
	}

	httpx.Respond(writer, http.StatusOK, res)
}

// respondError maps the service's typed failures (and the portal resolver's)
// onto the canonical error envelope.
func (api *nfceAPI) respondError(writer http.ResponseWriter, req *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrAlreadyCommitted):
		httpx.Error(writer, req, http.StatusConflict, "preview_already_committed",
			"this preview has already been committed")
	case errors.Is(err, service.ErrPreviewExpired):
		httpx.Error(writer, req, http.StatusUnprocessableEntity, "preview_expired",
			"this preview has expired; create a new one")
	case errors.Is(err, service.ErrPreviewNotFound):
		httpx.Error(writer, req, http.StatusNotFound, "preview_not_found",
			"preview not found")
	case errors.Is(err, service.ErrInvalidResolution):
		httpx.Error(writer, req, http.StatusBadRequest, "invalid_resolution", err.Error())
	case errors.Is(err, service.ErrReceiptNotFound),
		errors.Is(err, service.ErrForbidden),
		errors.Is(err, service.ErrInUse),
		errors.Is(err, service.ErrInvalidFilter):
		// Phase 7 receipt-lifecycle failures share the read/admin envelope.
		respondServiceError(writer, req, err)
	default:
		respondResolveError(writer, req, err)
	}
}

// subjectID extracts the authenticated user id from the request context
// (guaranteed populated by RequireAuth).
func subjectID(writer http.ResponseWriter, req *http.Request) (int64, bool) {
	claims := auth.Principal(req.Context())
	if claims == nil {
		httpx.Error(writer, req, http.StatusUnauthorized, "unauthorized", "missing bearer token")
		return 0, false
	}
	id, err := auth.SubjectID(claims)
	if err != nil {
		httpx.Error(writer, req, http.StatusUnauthorized, "unauthorized", "invalid token claims")
		return 0, false
	}
	return id, true
}

// respondResolveError maps the nfce pipeline failures onto the canonical error
// envelope. It is shared by /nfce/new and /nfce/submit, whose draft/commit
// re-resolve a receipt.
func respondResolveError(writer http.ResponseWriter, req *http.Request, err error) {
	var capErr *nfce.CaptchaError
	switch {
	case errors.As(err, &capErr):
		respondCaptcha(writer, capErr)
	case errors.Is(err, nfce.ErrKeyNotFound):
		httpx.Error(writer, req, http.StatusNotFound, "receipt_not_found",
			"no receipt was found for this access key")
	case errors.Is(err, nfce.ErrInvalidKey):
		httpx.Error(writer, req, http.StatusUnprocessableEntity, "invalid_access_key", err.Error())
	case errors.Is(err, nfce.ErrInvalidQRPayload):
		httpx.Error(writer, req, http.StatusUnprocessableEntity, "invalid_qr_payload", err.Error())
	case errors.Is(err, nfce.ErrUnsupportedUF):
		httpx.Error(writer, req, http.StatusNotImplemented, "unsupported_uf", err.Error())
	case errors.Is(err, nfce.ErrUpstream), errors.Is(err, nfce.ErrBadResponse):
		httpx.Error(writer, req, http.StatusBadGateway, "upstream_error",
			"the fiscal portal could not answer the request")
	default:
		httpx.InternalError(writer, req)
	}
}

// respondCaptcha returns the captcha challenge the client must solve. The
// envelope always uses the canonical error wrapper; captcha fields ride along
// so the SPA can render the image (proxied by our no-store endpoint) and echo
// the challenge id back in the solve call.
func respondCaptcha(w http.ResponseWriter, capErr *nfce.CaptchaError) {
	httpx.Respond(w, http.StatusPreconditionFailed, map[string]any{
		"error": map[string]any{
			"code":         "captcha_required",
			"message":      "the fiscal portal requires a captcha challenge; solve it and retry",
			"challenge_id": capErr.ChallengeID,
			"image_url":    "/api/v1/nfce/captcha/image/" + capErr.ChallengeID,
		},
	})
}
