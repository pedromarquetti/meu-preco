package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"meupreco/internal/auth"
	"meupreco/internal/config"
	"meupreco/internal/domain"
)

func hardeningRouter(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	return newRouter(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), stubResolver{}, &fakeStores{}, nil)
}

func bearer(t *testing.T, cfg config.Config, u *domain.User) string {
	t.Helper()
	tokens := auth.NewTokenService(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	token, err := tokens.IssueAccess(u)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return token
}

func doAuthed(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	hardeningRouter(t, config.Config{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for h, v := range want {
		if got := rec.Header().Get(h); got != v {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}
}

func TestMetricsEndpoint(t *testing.T) {
	rec := httptest.NewRecorder()
	hardeningRouter(t, config.Config{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := body["requests"]; !ok {
		t.Errorf("missing requests report: %v", body)
	}
	if _, ok := body["off_cache"]; ok {
		t.Errorf("off_cache leaked while enrichment disabled: %v", body)
	}
}

func TestMetricsEndpointIncludesOFFStatsWhenEnabled(t *testing.T) {
	rec := httptest.NewRecorder()
	hardeningRouter(t, config.Config{EnableOFFEnrich: true}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := body["off_cache"]; !ok {
		t.Errorf("off_cache missing while enrichment enabled: %v", body)
	}
}

func TestPerIPRateLimitReturnsSharedEnvelope(t *testing.T) {
	cfg := config.Config{
		JWTSecret:        testJWTSecret,
		JWTAccessTTL:     30 * time.Minute,
		JWTRefreshTTL:    24 * time.Hour,
		RateLimitIPRPS:   1,
		RateLimitIPBurst: 1,
	}
	h := hardeningRouter(t, cfg)
	token := bearer(t, cfg, &domain.User{ID: 7, Email: "u@example.com", Role: "user"})

	if rec := doAuthed(h, "/api/v1/users/me", token); rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec.Code)
	}
	rec := doAuthed(h, "/api/v1/users/me", token)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 response missing Retry-After")
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Error.Code != "rate_limited" {
		t.Errorf("error code = %q, want rate_limited", body.Error.Code)
	}
}

func TestPerUserRateLimit(t *testing.T) {
	cfg := config.Config{
		JWTSecret:          testJWTSecret,
		JWTAccessTTL:       30 * time.Minute,
		JWTRefreshTTL:      24 * time.Hour,
		RateLimitUserRPS:   1,
		RateLimitUserBurst: 1,
	}
	h := hardeningRouter(t, cfg)
	alice := bearer(t, cfg, &domain.User{ID: 1, Email: "alice@example.com", Role: "user"})
	bob := bearer(t, cfg, &domain.User{ID: 2, Email: "bob@example.com", Role: "user"})

	if rec := doAuthed(h, "/api/v1/users/me", alice); rec.Code != http.StatusOK {
		t.Fatalf("alice first: %d, want 200", rec.Code)
	}
	if rec := doAuthed(h, "/api/v1/users/me", alice); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("alice second: %d, want 429", rec.Code)
	}
	// Bob has his own bucket: Alice's exhaustion must not throttle him.
	if rec := doAuthed(h, "/api/v1/users/me", bob); rec.Code != http.StatusOK {
		t.Fatalf("bob first: %d, want 200", rec.Code)
	}
}

func TestRateLimitDisabledByZeroConfig(t *testing.T) {
	cfg := config.Config{JWTSecret: testJWTSecret, JWTAccessTTL: 30 * time.Minute, JWTRefreshTTL: 24 * time.Hour}
	h := hardeningRouter(t, cfg)
	token := bearer(t, cfg, &domain.User{ID: 7, Email: "u@example.com", Role: "user"})
	for i := 0; i < 50; i++ {
		if rec := doAuthed(h, "/api/v1/users/me", token); rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 (limiter should be disabled)", i, rec.Code)
		}
	}
}
