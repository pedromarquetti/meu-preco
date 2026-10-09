package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"meupreco/internal/httpx"
	"meupreco/internal/nfce"
	"meupreco/internal/service"
)

// captchaAPI serves the challenge endpoints, all behind RequireAuth
// (same group as the ingestion endpoints):
//
//	GET  /api/v1/nfce/captcha               — challenge state
//	POST /api/v1/nfce/captcha               — solve: submit the typed answer
//	GET  /api/v1/nfce/captcha/image/{id}    — challenge image proxy
//
// The challenge itself is minted by the portal layer: the first /nfce/new on a
// receipt that throws a captcha answers 412 with challenge_id + image_url;
// these endpoints are the client's way to look at that state, load the image,
// and turn a correct solve into the pending preview (/nfce/new then commits).
type captchaAPI struct {
	svc     *service.Service
	log     *slog.Logger
	limiter *ipLimiter
}

// newCaptchaAPI mounts the solve endpoints. The service is shared with the
// ingestion API so reports and captcha solves see the same pipeline.
func newCaptchaAPI(svc *service.Service, log *slog.Logger) *captchaAPI {
	return &captchaAPI{
		svc:     svc,
		log:     log,
		limiter: newIPLimiter(30, time.Minute),
	}
}

// ipLimiter is a fixed-window per-IP request budget over the solve endpoints,
// so a distractor cannot burn the whole process on challenge solves. The
// per-challenge attempt budget (5) already caps one challenge; this caps the
// number of challenges one IP may touch per window.
type ipLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	counts map[string]ipEntry
	swept  time.Time
	now    func() time.Time
}

type ipEntry struct {
	count int
	reset time.Time
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{
		limit:  limit,
		window: window,
		counts: make(map[string]ipEntry),
		now:    time.Now,
	}
}

// Allow returns true if the IP still has budget for one more request, charging
// one. Stale windows are dropped on the way in so the map stays bounded.
func (l *ipLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.swept.IsZero() || now.After(l.swept.Add(l.window)) {
		for k, e := range l.counts {
			if now.After(e.reset) {
				delete(l.counts, k)
			}
		}
		l.swept = now
	}
	e, ok := l.counts[ip]
	if !ok || now.After(e.reset) {
		e = ipEntry{reset: now.Add(l.window)}
	}
	if e.count >= l.limit {
		return false
	}
	e.count++
	l.counts[ip] = e
	return true
}

func (api *captchaAPI) state(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get("challenge_id")
	if id == "" {
		httpx.Error(w, req, http.StatusBadRequest, "invalid_request",
			"a challenge_id query parameter is required")
		return
	}
	info, err := api.svc.ChallengeStatus(req.Context(), id)
	if err != nil {
		api.respondSolveError(w, req, err)
		return
	}
	httpx.Respond(w, http.StatusOK, info)
}

func (api *captchaAPI) solve(w http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(w, req.Body, maxNfceBody)

	var body struct {
		ChallengeID string `json:"challenge_id"`
		Solution    string `json:"solution"`
	}
	if err := decodeJSON(req, &body); err != nil {
		httpx.Error(w, req, http.StatusBadRequest, "invalid_request",
			`body must be valid JSON: {"challenge_id","solution"}`)
		return
	}

	userID, ok := subjectID(w, req)
	if !ok {
		return
	}

	if !api.limiter.Allow(clientIP(req)) {
		httpx.Error(w, req, http.StatusTooManyRequests, "rate_limited",
			"too many captcha requests; slow down and retry")
		return
	}

	res, err := api.svc.SolveCaptcha(req.Context(), service.SolveCaptchaRequest{
		UserID:      userID,
		ChallengeID: body.ChallengeID,
		Solution:    body.Solution,
	})
	if err != nil {
		api.respondSolveError(w, req, err)
		return
	}

	// A solved challenge yields the draft the receipt was blocked on: the
	// same pending preview /nfce/new would have produced, ready to confirm.
	httpx.Respond(w, http.StatusOK, res)
}

func (api *captchaAPI) image(w http.ResponseWriter, req *http.Request) {
	id := chi.URLParam(req, "challenge_id")
	if id == "" {
		httpx.Error(w, req, http.StatusBadRequest, "invalid_request", "missing challenge id")
		return
	}

	data, ctype, err := api.svc.ChallengeImage(req.Context(), id)
	if err != nil {
		api.respondSolveError(w, req, err)
		return
	}

	// The bytes belong to the SEFAZ session and must never be cached (they
	// carry no intrinsic order/session identity of their own).
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// respondSolveError maps the captcha flow's typed failures onto the canonical
// envelope. The status-and-code contract (from PROJECT.md):
//
//   - wrong solution that still allows retries  → 412 captcha_wrong_solution
//   - challenge not found                      → 404 challenge_not_found
//   - challenge expired                        → 422 challenge_expired
//   - challenge locked (budget spent)          → 429 challenge_locked
//   - challenge already solved (single-use)    → 409 challenge_used
//
// 412 envelopes carry the challenge again so the client can immediately render
// the (possibly refreshed) image and echo the id back on the next attempt.
func (api *captchaAPI) respondSolveError(w http.ResponseWriter, req *http.Request, err error) {
	var wrong *service.SolutionIncorrectError
	switch {
	case errors.As(err, &wrong):
		info, infoErr := api.svc.ChallengeStatus(req.Context(), wrong.ChallengeID)
		if infoErr != nil || info == nil || info.Status == "solved" {
			httpx.InternalError(w, req)
			return
		}
		httpx.Respond(w, http.StatusPreconditionFailed, map[string]any{
			"error": map[string]any{
				"code":          "captcha_wrong_solution",
				"message":       "the solution was rejected; check the image and retry",
				"challenge_id":  info.ID,
				"attempts_left": info.AttemptsLeft,
				"image_url":     info.ImageURL,
			},
		})
	case errors.Is(err, nfce.ErrChallengeNotFound):
		httpx.Error(w, req, http.StatusNotFound, "challenge_not_found",
			"no active captcha challenge with this id")
	case errors.Is(err, nfce.ErrChallengeExpired):
		httpx.Error(w, req, http.StatusUnprocessableEntity, "challenge_expired",
			"this captcha challenge expired; re-fetch the receipt to get a fresh one")
	case errors.Is(err, nfce.ErrChallengeLocked):
		httpx.Error(w, req, http.StatusTooManyRequests, "challenge_locked",
			"too many wrong answers for this challenge; re-fetch the receipt")
	case errors.Is(err, nfce.ErrChallengeReused):
		httpx.Error(w, req, http.StatusConflict, "challenge_used",
			"this challenge was already solved; the receipt is already drafted under this session")
	default:
		httpx.InternalError(w, req)
	}
}

// decodeJSON is the small JSON reader every handler here shares.
func decodeJSON(req *http.Request, v any) error {
	return json.NewDecoder(req.Body).Decode(v)
}

// clientIP is the remote address as ip:port, port stripped.
func clientIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}
