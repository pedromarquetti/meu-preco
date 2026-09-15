package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"meupreco/internal/domain"
	"meupreco/internal/httpx"
	"meupreco/internal/nfce"
)

const maxSubmitBody = 1 << 16 // 64 KiB is plenty for {"access_key": "..."}

// nfceResolver is the slice of the nfce pipeline the submit handler needs.
// Keeping it an interface lets httptest tests stub the whole portal layer.
type nfceResolver interface {
	Resolve(ctx context.Context, key nfce.AccessKey) (*domain.Nfce, error)
}

type submitRequest struct {
	AccessKey string `json:"access_key"`
}

// handleNfceSubmit implements POST /api/v1/nfce/submit: normalize + validate the
// chave, fetch the DANFE from the portal, parse it, and return the normalized
// (privacy-stripped) receipt. Captcha challenges are returned as a typed 412.
//
// This endpoint intentionally owns no persistence — Phase 2 (DB) and Phase 5
// (preview/commit) build on top of it.
func handleNfceSubmit(res nfceResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxSubmitBody)

		var req submitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid_request",
				`body must be JSON: {"access_key": "<44-digit chave>"}`)
			return
		}

		key, err := nfce.ParseAccessKey(req.AccessKey)
		if err != nil {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "invalid_access_key", err.Error())
			return
		}

		receipt, err := res.Resolve(r.Context(), key)
		if err != nil {
			var capErr *nfce.CaptchaError
			switch {
			case errors.As(err, &capErr):
				respondCaptcha(w, capErr)
			case errors.Is(err, nfce.ErrKeyNotFound):
				httpx.Error(w, r, http.StatusNotFound, "receipt_not_found",
					"no receipt was found for this access key")
			case errors.Is(err, nfce.ErrInvalidKey):
				httpx.Error(w, r, http.StatusUnprocessableEntity, "invalid_access_key", err.Error())
			case errors.Is(err, nfce.ErrUpstream), errors.Is(err, nfce.ErrBadResponse):
				httpx.Error(w, r, http.StatusBadGateway, "upstream_error",
					"the fiscal portal could not answer the request")
			default:
				httpx.InternalError(w, r)
			}
			return
		}

		httpx.Respond(w, http.StatusOK, map[string]*domain.Nfce{"receipt": receipt})
	}
}

// respondCaptcha returns the captcha challenge the client must solve. The
// envelope always uses the canonical error wrapper; captcha fields ride along
// so the SPA can render the image and echo the challenge id back in Phase 6.
func respondCaptcha(w http.ResponseWriter, capErr *nfce.CaptchaError) {
	httpx.Respond(w, http.StatusPreconditionFailed, map[string]any{
		"error": map[string]any{
			"code":         "captcha_required",
			"message":      "the fiscal portal requires a captcha challenge; solve it and retry",
			"challenge_id": capErr.ChallengeID,
			"image_url":    capErr.ImageURL,
		},
	})
}
