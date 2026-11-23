package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func mustPrefixes(t *testing.T, raw ...string) []netip.Prefix {
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

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRealIPResolver(t *testing.T) {
	// Loopback plus the Docker network a proxy would live on.
	rr := NewRealIPResolver(mustPrefixes(t,
		"127.0.0.1/32",
		"::1/128",
		"172.18.0.0/16",
	), discardLogger())

	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "direct untrusted client ignores forged XFF",
			remoteAddr: "203.0.113.9:1234",
			headers:    map[string]string{"X-Forwarded-For": "1.2.3.4"},
			want:       "203.0.113.9",
		},
		{
			name:       "direct untrusted client ignores forged CF header",
			remoteAddr: "203.0.113.9:1234",
			headers:    map[string]string{"CF-Connecting-IP": "1.2.3.4"},
			want:       "203.0.113.9",
		},
		{
			name:       "trusted loopback single XFF",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.9"},
			want:       "203.0.113.9",
		},
		{
			name:       "trusted chain with known proxy on the right",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.9, 172.18.0.1"},
			want:       "203.0.113.9",
		},
		{
			name:       "unknown proxy in chain stops the walk",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.9, 198.51.100.7"},
			want:       "198.51.100.7",
		},
		{
			name:       "no XFF falls back to CF-Connecting-IP",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"CF-Connecting-IP": "203.0.113.9"},
			want:       "203.0.113.9",
		},
		{
			name:       "no XFF falls back to True-Client-IP",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"True-Client-IP": "203.0.113.9"},
			want:       "203.0.113.9",
		},
		{
			name:       "no XFF falls back to X-Real-IP",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Real-IP": "203.0.113.9"},
			want:       "203.0.113.9",
		},
		{
			name:       "RFC 7239 Forwarded",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"Forwarded": "for=203.0.113.9;proto=https"},
			want:       "203.0.113.9",
		},
		{
			name:       "RFC 7239 quoted IPv6 with port",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"Forwarded": `for="[2001:db8::1]:443";proto=https`},
			want:       "2001:db8::1",
		},
		{
			name:       "IPv6 with port in XFF",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Forwarded-For": "[2001:db8::1]:443"},
			want:       "2001:db8::1",
		},
		{
			name:       "quoted IPv6 in XFF",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Forwarded-For": `"[2001:db8::1]:443"`},
			want:       "2001:db8::1",
		},
		{
			name:       "IPv4-mapped peer is treated as loopback",
			remoteAddr: "[::ffff:127.0.0.1]:1234",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.9"},
			want:       "203.0.113.9",
		},
		{
			name:       "malformed XFF entry is skipped",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Forwarded-For": "not-an-ip, 203.0.113.9"},
			want:       "203.0.113.9",
		},
		{
			name:       "all-known XFF falls through to X-Real-IP",
			remoteAddr: "127.0.0.1:1234",
			headers: map[string]string{
				"X-Forwarded-For": "172.18.0.1, 127.0.0.1",
				"X-Real-IP":       "203.0.113.9",
			},
			want: "203.0.113.9",
		},
		{
			name:       "all-known XFF with no fallback returns peer",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Forwarded-For": "172.18.0.1, 127.0.0.1"},
			want:       "127.0.0.1",
		},
		{
			name:       "malformed-only XFF returns peer",
			remoteAddr: "127.0.0.1:1234",
			headers:    map[string]string{"X-Forwarded-For": "not-an-ip"},
			want:       "127.0.0.1",
		},
		{
			name:       "peer without a port",
			remoteAddr: "203.0.113.9",
			headers:    map[string]string{"X-Forwarded-For": "1.2.3.4"},
			want:       "203.0.113.9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			if got := rr.Resolve(req); got != tc.want {
				t.Errorf("Resolve() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRealIPResolverHugeXFFIsBounded(t *testing.T) {
	rr := NewRealIPResolver(mustPrefixes(t, "127.0.0.1/32"), discardLogger())

	var b strings.Builder
	for i := 0; i < 10_000; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("172.18.0.1")
	}
	b.WriteString(", 203.0.113.9")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", b.String())

	if got := rr.Resolve(req); got != "203.0.113.9" {
		t.Errorf("Resolve() = %q, want 203.0.113.9", got)
	}
}

func TestClientIPUsesResolvedContext(t *testing.T) {
	rr := NewRealIPResolver(mustPrefixes(t, "127.0.0.1/32"), discardLogger())

	var got string
	h := RealIP(rr)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = ClientIP(r)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != "203.0.113.9" {
		t.Errorf("ClientIP = %q, want 203.0.113.9", got)
	}
}

func TestClientIPFallsBackWithoutMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:5678"

	if got := ClientIP(req); got != "203.0.113.9" {
		t.Errorf("ClientIP = %q, want 203.0.113.9", got)
	}

	req.RemoteAddr = "203.0.113.9"
	if got := ClientIP(req); got != "203.0.113.9" {
		t.Errorf("ClientIP without port = %q, want 203.0.113.9", got)
	}
}

func TestRealIPWarnsOnceForUntrustedPeer(t *testing.T) {
	var buf bytes.Buffer
	rr := NewRealIPResolver(mustPrefixes(t, "127.0.0.1/32"), testLogger(&buf))

	h := RealIP(rr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "203.0.113.9:1234"
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}

	if n := strings.Count(buf.String(), "proxy headers ignored"); n != 1 {
		t.Errorf("warning count = %d, want 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "203.0.113.9") {
		t.Errorf("warning missing peer:\n%s", buf.String())
	}
}

func TestLoggingIncludesResolvedClientIP(t *testing.T) {
	var buf bytes.Buffer
	log := testLogger(&buf)

	r := chi.NewRouter()
	r.Use(
		RequestID,
		RealIP(NewRealIPResolver(mustPrefixes(t, "127.0.0.1/32"), log)),
		Logging(log),
	)
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.ServeHTTP(httptest.NewRecorder(), req)

	logs := buf.String()
	for _, want := range []string{
		"client_ip=203.0.113.9",
		"remote_addr=127.0.0.1:1234",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("log missing %q:\n%s", want, logs)
		}
	}
}
