package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

func newTestClock() (func() time.Time, func(time.Duration)) {
	t := time.Unix(1_700_000_000, 0)
	return func() time.Time { return t }, func(d time.Duration) { t = t.Add(d) }
}

func TestTokenBucketAllowsBurstThenRefills(t *testing.T) {
	now, advance := newTestClock()
	b := newTokenBucket(1, 3, now)

	if !b.allow() || !b.allow() || !b.allow() {
		t.Fatal("burst of 3 should pass")
	}
	if b.allow() {
		t.Fatal("fourth token before refill should be rejected")
	}
	advance(time.Second)
	if !b.allow() {
		t.Fatal("refilled token should pass")
	}
}

func TestKeyedRateLimitReturns429Envelope(t *testing.T) {
	r := chi.NewRouter()
	r.Use(KeyedRateLimit(0.01, 1, IPKey))
	r.Get("/", okHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:5678"

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first request status = %d, want 204", rec.Code)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"rate_limited"`) {
		t.Errorf("body = %s, want rate_limited envelope", rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header not set")
	}
}

func TestKeyedRateLimitIsPerKey(t *testing.T) {
	r := chi.NewRouter()
	r.Use(KeyedRateLimit(0.01, 1, IPKey))
	r.Get("/", okHandler)

	a := httptest.NewRequest(http.MethodGet, "/", nil)
	a.RemoteAddr = "198.51.100.1:1000"
	b := httptest.NewRequest(http.MethodGet, "/", nil)
	b.RemoteAddr = "198.51.100.2:2000"

	for i, tc := range []struct {
		req  *http.Request
		want int
	}{
		{a, http.StatusNoContent}, // ip:1 consumes its only token
		{b, http.StatusNoContent}, // a different IP has its own bucket
		{a, http.StatusTooManyRequests},
		{b, http.StatusTooManyRequests},
		{a, http.StatusTooManyRequests},
		{b, http.StatusTooManyRequests},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, tc.req)
		if rec.Code != tc.want {
			t.Errorf("step %d (%s) status = %d, want %d", i, tc.req.RemoteAddr, rec.Code, tc.want)
		}
	}
}

func TestKeyedRateLimitDisabled(t *testing.T) {
	mw := KeyedRateLimit(0, 0, IPKey)
	if got := mw(http.HandlerFunc(okHandler)); got == nil {
		t.Fatal("disabled middleware returned nil")
	}
}

func TestClientIPStripsPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.0.2.9:443"
	if got := ClientIP(r); got != "192.0.2.9" {
		t.Errorf("ClientIP = %q, want 192.0.2.9", got)
	}
}

func TestKeyedLimiterSweepsIdleBuckets(t *testing.T) {
	now, advance := newTestClock()
	l := newKeyedLimiter(1, 1, now)
	if !l.allow("ip:a") {
		t.Fatal("first allow should pass")
	}
	advance(6 * time.Minute)
	l.allow("ip:b") // triggers the idle sweep
	if len(l.buckets) != 1 {
		t.Fatalf("buckets = %d, want 1 after sweeping idle ip:a", len(l.buckets))
	}
	if _, ok := l.buckets["ip:a"]; ok {
		t.Error("idle ip:a bucket not swept")
	}
}
