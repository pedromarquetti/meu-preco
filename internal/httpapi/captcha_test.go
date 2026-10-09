package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"meupreco/internal/config"
	"meupreco/internal/nfce"
	"meupreco/internal/service"
)

// fakeSolver is a hermetic nfce.CaptchaSolver: "OKAY" is the one correct
// answer; anything else replays the fixture challenge page as a rejection. The
// image endpoint serves fixed bytes, proving the proxy path end to end.
type fakeSolver struct {
	danfe []byte
	wrong []byte
	image []byte
	ctype string
}

func (f *fakeSolver) Solve(_ context.Context, _ *nfce.Challenge, solution string) ([]byte, error) {
	if solution == "OKAY" {
		return f.danfe, nil
	}
	return f.wrong, nfce.ErrWrongSolution
}

func (f *fakeSolver) Image(_ context.Context, _ *nfce.Challenge) ([]byte, string, error) {
	return f.image, f.ctype, nil
}

func newFakeSolver(t *testing.T) *fakeSolver {
	t.Helper()
	danfe, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatalf("read danfe fixture: %v", err)
	}
	wrong, err := os.ReadFile("../../testdata/captcha.html")
	if err != nil {
		t.Fatalf("read captcha fixture: %v", err)
	}
	return &fakeSolver{
		danfe: danfe,
		wrong: wrong,
		image: []byte("fakepng"),
		ctype: "image/png",
	}
}

// captchaHarness is a full authenticated router backed by a ChallengeStore the
// tests can seed challenges into.
type captchaHarness struct {
	t     *testing.T
	h     http.Handler
	store *nfce.ChallengeStore
}

func newCaptchaHarness(t *testing.T, solver nfce.CaptchaSolver) *captchaHarness {
	t.Helper()
	store := nfce.NewChallengeStore()
	h := authRouter(t, newStubStores(), stubResolver{receipt: uidUncertainReceipt()}, 1,
		&service.CaptchaSupport{Store: store, Solver: solver, MaxAttempts: 5})
	return &captchaHarness{t: t, h: h, store: store}
}

func (h *captchaHarness) seed(accessKey string) string {
	h.t.Helper()
	return h.store.Register(&nfce.Challenge{AccessKey: accessKey}).ID
}

func (h *captchaHarness) post(body string) *httptest.ResponseRecorder {
	h.t.Helper()
	return postNfce(h.t, h.h, "/api/v1/nfce/captcha", body)
}

func (h *captchaHarness) get(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	h.h.ServeHTTP(rec, req)
	return rec
}

func TestCaptchaSolveCreatesPendingDraft(t *testing.T) {
	h := newCaptchaHarness(t, newFakeSolver(t))
	id := h.seed(senateKey)

	rec := h.post(`{"challenge_id":"` + id + `","solution":"OKAY"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var plan struct {
		PreviewID int64  `json:"preview_id"`
		Status    string `json:"status"`
		AccessKey string `json:"access_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if plan.PreviewID == 0 || plan.Status != "pending" {
		t.Errorf("plan = %+v, want a pending preview", plan)
	}
	if plan.AccessKey != senateKey {
		t.Errorf("access_key = %q, want %q", plan.AccessKey, senateKey)
	}

	// The challenge is single-use: a replay of the same solve must 409.
	replay := h.post(`{"challenge_id":"` + id + `","solution":"OKAY"}`)
	if replay.Code != http.StatusConflict {
		t.Fatalf("replay status = %d, want 409", replay.Code)
	}
	if code, _ := decodeErrorEnvelope(t, replay); code != "challenge_used" {
		t.Errorf("replay code = %q", code)
	}

	// The solved state is now reflected on GET /captcha.
	state := h.get("/api/v1/nfce/captcha?challenge_id=" + id)
	if state.Code != http.StatusOK {
		t.Fatalf("state status = %d", state.Code)
	}
	var info struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(state.Body.Bytes(), &info); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if info.Status != "solved" {
		t.Errorf("state status = %q, want solved", info.Status)
	}
}

func TestCaptchaSolveWrongAnswerRetries(t *testing.T) {
	h := newCaptchaHarness(t, newFakeSolver(t))
	id := h.seed(senateKey)

	rec := h.post(`{"challenge_id":"` + id + `","solution":"NOPE"}`)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	code, extra := decodeErrorEnvelope(t, rec)
	if code != "captcha_wrong_solution" {
		t.Errorf("code = %q", code)
	}
	if extra["challenge_id"] != id {
		t.Errorf("challenge_id = %v", extra["challenge_id"])
	}
	if extra["attempts_left"].(float64) != 4 {
		t.Errorf("attempts_left = %v, want 4", extra["attempts_left"])
	}
}

func TestCaptchaSolveLocked(t *testing.T) {
	h := newCaptchaHarness(t, newFakeSolver(t))
	id := h.seed(senateKey)
	for i := 0; i < 5; i++ {
		if _, err := h.store.RecordFailure(id, nil); err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
	}

	rec := h.post(`{"challenge_id":"` + id + `","solution":"OKAY"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "challenge_locked" {
		t.Errorf("code = %q", code)
	}
}

func TestCaptchaSolveExpired(t *testing.T) {
	h := newCaptchaHarness(t, newFakeSolver(t))
	id := h.store.Register(&nfce.Challenge{
		AccessKey: senateKey,
		TTL:       time.Now().Add(-time.Second),
	}).ID

	rec := h.post(`{"challenge_id":"` + id + `","solution":"OKAY"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "challenge_expired" {
		t.Errorf("code = %q", code)
	}
}

func TestCaptchaSolveNotFound(t *testing.T) {
	h := newCaptchaHarness(t, newFakeSolver(t))
	rec := h.post(`{"challenge_id":"does-not-exist","solution":"OKAY"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if code, _ := decodeErrorEnvelope(t, rec); code != "challenge_not_found" {
		t.Errorf("code = %q", code)
	}
}

func TestCaptchaRateLimitedPerIP(t *testing.T) {
	h := newCaptchaHarness(t, newFakeSolver(t))
	_ = h.seed(senateKey)

	var last *httptest.ResponseRecorder
	for i := 0; i <= 30; i++ {
		last = h.post(`{"challenge_id":"no-such-` + string(rune('a'+i)) + `","solution":"X"}`)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("31st status = %d, want 429", last.Code)
	}
	if code, _ := decodeErrorEnvelope(t, last); code != "rate_limited" {
		t.Errorf("code = %q", code)
	}
}

func TestCaptchaImageProxiedNoStore(t *testing.T) {
	h := newCaptchaHarness(t, newFakeSolver(t))
	id := h.seed(senateKey)

	rec := h.get("/api/v1/nfce/captcha/image/" + id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "fakepng" {
		t.Errorf("body = %q, want proxied bytes", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content-type = %q, want image/png", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("cache-control = %q, want no-store (captcha bytes must not be cached)", cc)
	}
}

func TestCaptchaImageRejectedAfterSolve(t *testing.T) {
	h := newCaptchaHarness(t, newFakeSolver(t))
	id := h.seed(senateKey)
	if rec := h.post(`{"challenge_id":"` + id + `","solution":"OKAY"}`); rec.Code != http.StatusOK {
		t.Fatalf("solve status = %d", rec.Code)
	}

	rec := h.get("/api/v1/nfce/captcha/image/" + id)
	if rec.Code != http.StatusConflict {
		t.Fatalf("image after solve status = %d, want 409", rec.Code)
	}
}

func TestCaptchaStatePending(t *testing.T) {
	h := newCaptchaHarness(t, newFakeSolver(t))
	id := h.seed(senateKey)

	rec := h.get("/api/v1/nfce/captcha?challenge_id=" + id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var info struct {
		ID           string  `json:"challenge_id"`
		Status       string  `json:"status"`
		AttemptsLeft float64 `json:"attempts_left"`
		MaxAttempts  float64 `json:"max_attempts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.ID != id || info.Status != "pending" || info.AttemptsLeft != 5 || info.MaxAttempts != 5 {
		t.Errorf("info = %+v, want pending challenge with 5 attempts", info)
	}
}

func TestCaptchaSolveRequiresAuth(t *testing.T) {
	cfg := config.Config{JWTSecret: "overridden-tests"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := nfce.NewChallengeStore()
	raw := newRouter(cfg, log, stubResolver{receipt: uidUncertainReceipt()}, newStubStores(),
		&service.CaptchaSupport{Store: store, Solver: newFakeSolver(t), MaxAttempts: 5})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/nfce/captcha",
		strings.NewReader(`{"challenge_id":"x","solution":"y"}`))
	rec := httptest.NewRecorder()
	raw.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
