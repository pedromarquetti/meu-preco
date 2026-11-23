package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
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

func doAuthedReq(h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
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

// hardeningProxyPrefixes builds the trusted-proxy list used by the tests that
// exercise real-IP resolution (loopback is the test client's peer).
func hardeningProxyPrefixes(t *testing.T, raw ...string) []netip.Prefix {
	t.Helper()

	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			t.Fatalf("ParsePrefix(%q): %v", s, err)
		}
		prefixes = append(prefixes, p)
	}
	return prefixes
}

// doFromPeer sends a request from the trusted loopback peer with the given
// resolved client IP (via X-Forwarded-For). An empty clientIP leaves the
// header off so the peer itself is the client.
func doFromPeer(h http.Handler, method, path, token, clientIP string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:2345"
	if clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIPKeyUsesResolvedClientIP(t *testing.T) {
	cfg := config.Config{
		JWTSecret:        testJWTSecret,
		JWTAccessTTL:     30 * time.Minute,
		JWTRefreshTTL:    24 * time.Hour,
		KnownProxies:     hardeningProxyPrefixes(t, "127.0.0.1/32"),
		RateLimitIPRPS:   0.001,
		RateLimitIPBurst: 1,
	}
	h := hardeningRouter(t, cfg)
	token := bearer(t, cfg, &domain.User{ID: 7, Email: "u@example.com", Role: "user"})

	if rec := doFromPeer(h, http.MethodGet, "/api/v1/auth/me", token, "203.0.113.1"); rec.Code != http.StatusOK {
		t.Fatalf("client A first = %d, want 200", rec.Code)
	}
	if rec := doFromPeer(h, http.MethodGet, "/api/v1/auth/me", token, "203.0.113.1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("client A second = %d, want 429", rec.Code)
	}
	// A different resolved client IP must not share A's bucket.
	if rec := doFromPeer(h, http.MethodGet, "/api/v1/auth/me", token, "203.0.113.2"); rec.Code != http.StatusOK {
		t.Fatalf("client B first = %d, want 200", rec.Code)
	}
}

func TestAuthBucketIsStricterThanGlobal(t *testing.T) {
	cfg := config.Config{
		JWTSecret:            testJWTSecret,
		JWTAccessTTL:         30 * time.Minute,
		JWTRefreshTTL:        24 * time.Hour,
		KnownProxies:         hardeningProxyPrefixes(t, "127.0.0.1/32"),
		RateLimitIPRPS:       1,
		RateLimitIPBurst:     10,
		RateLimitAuthIPRPS:   0.001,
		RateLimitAuthIPBurst: 1,
	}
	h := hardeningRouter(t, cfg)

	if rec := doFromPeer(h, http.MethodPost, "/api/v1/auth/login", "", "203.0.113.5"); rec.Code == http.StatusTooManyRequests {
		t.Fatalf("first login was throttled: %d (want the auth bucket to allow it)", rec.Code)
	}
	if rec := doFromPeer(h, http.MethodPost, "/api/v1/auth/login", "", "203.0.113.5"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second login = %d, want 429 even though the global bucket has tokens", rec.Code)
	}

	// The global bucket still has budget for a non-auth route from the same IP.
	token := bearer(t, cfg, &domain.User{ID: 7, Email: "u@example.com", Role: "user"})
	if rec := doFromPeer(h, http.MethodGet, "/api/v1/auth/me", token, "203.0.113.5"); rec.Code != http.StatusOK {
		t.Fatalf("non-auth request = %d, want 200 (global budget should remain)", rec.Code)
	}
}

func TestAuthBucketCoversEveryAuthRoute(t *testing.T) {
	cfg := config.Config{
		JWTSecret:            testJWTSecret,
		JWTAccessTTL:         30 * time.Minute,
		JWTRefreshTTL:        24 * time.Hour,
		KnownProxies:         hardeningProxyPrefixes(t, "127.0.0.1/32"),
		RateLimitAuthIPRPS:   0.001,
		RateLimitAuthIPBurst: 1,
	}
	h := hardeningRouter(t, cfg)

	// The first auth request spends the single token; every other auth route
	// from the same IP is then throttled by the shared bucket.
	if rec := doFromPeer(h, http.MethodPost, "/api/v1/auth/login", "", "203.0.113.5"); rec.Code == http.StatusTooManyRequests {
		t.Fatalf("first auth request was throttled: %d", rec.Code)
	}
	for _, path := range []string{
		"/api/v1/auth/register",
		"/api/v1/auth/login",
		"/api/v1/auth/refresh",
	} {
		if rec := doFromPeer(h, http.MethodPost, path, "", "203.0.113.5"); rec.Code != http.StatusTooManyRequests {
			t.Errorf("%s = %d, want 429", path, rec.Code)
		}
	}
}

func TestAuthBucketIsPerRealIP(t *testing.T) {
	cfg := config.Config{
		JWTSecret:            testJWTSecret,
		JWTAccessTTL:         30 * time.Minute,
		JWTRefreshTTL:        24 * time.Hour,
		KnownProxies:         hardeningProxyPrefixes(t, "127.0.0.1/32"),
		RateLimitAuthIPRPS:   0.001,
		RateLimitAuthIPBurst: 1,
	}
	h := hardeningRouter(t, cfg)

	if rec := doFromPeer(h, http.MethodPost, "/api/v1/auth/login", "", "203.0.113.5"); rec.Code == http.StatusTooManyRequests {
		t.Fatalf("client A first = %d, want it allowed", rec.Code)
	}
	if rec := doFromPeer(h, http.MethodPost, "/api/v1/auth/login", "", "203.0.113.5"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("client A second = %d, want 429", rec.Code)
	}
	// Another client behind the same proxy has an independent auth bucket.
	if rec := doFromPeer(h, http.MethodPost, "/api/v1/auth/login", "", "203.0.113.6"); rec.Code == http.StatusTooManyRequests {
		t.Fatalf("client B first = %d, want it allowed", rec.Code)
	}
}

func TestPerUserBucketCoversEveryAuthenticatedGroup(t *testing.T) {
	cfg := config.Config{
		JWTSecret:          testJWTSecret,
		JWTAccessTTL:       30 * time.Minute,
		JWTRefreshTTL:      24 * time.Hour,
		RateLimitUserRPS:   1,
		RateLimitUserBurst: 1,
	}
	h := hardeningRouter(t, cfg)

	// An admin subject so the /admin group passes RequireRole and reaches the
	// per-user limiter.
	admin := bearer(t, cfg, &domain.User{ID: 11, Email: "admin@example.com", Role: "admin"})

	if rec := doAuthed(h, "/api/v1/auth/me", admin); rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}
	if rec := doAuthed(h, "/api/v1/auth/me", admin); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", rec.Code)
	}

	groups := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodGet, "/api/v1/users/me"},
		{http.MethodGet, "/api/v1/products/"},
		{http.MethodGet, "/api/v1/supermarkets/"},
		{http.MethodPost, "/api/v1/nfce/new"},
		{http.MethodGet, "/api/v1/admin/users"},
	}
	for _, g := range groups {
		if rec := doAuthedReq(h, g.method, g.path, admin); rec.Code != http.StatusTooManyRequests {
			t.Errorf("%s %s = %d, want 429 (per-user bucket missing)", g.method, g.path, rec.Code)
		}
	}
}

func TestPerUserBucketIsPerSubjectAcrossGroups(t *testing.T) {
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

	if rec := doAuthed(h, "/api/v1/auth/me", alice); rec.Code != http.StatusOK {
		t.Fatalf("alice first = %d, want 200", rec.Code)
	}
	if rec := doAuthed(h, "/api/v1/auth/me", alice); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("alice second = %d, want 429", rec.Code)
	}
	// Bob's budget is independent even on a different authenticated group.
	if rec := doAuthed(h, "/api/v1/users/me", bob); rec.Code != http.StatusOK {
		t.Fatalf("bob first = %d, want 200", rec.Code)
	}
}
