package nfce

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serveFixture(t *testing.T, file string) *httptest.Server {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Query().Get("p"), senateKey+"|") {
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(raw)
	}))
}

func TestHTTPSourceFetchOK(t *testing.T) {
	srv := serveFixture(t, "nfce_1.html")
	defer srv.Close()

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        100,
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	key, _ := ParseAccessKey(senateKey)
	body, err := src.Fetch(context.Background(), key, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !pageHasDANFE(body) {
		t.Errorf("fetched body is not a DANFE")
	}
}

func TestHTTPSourceFetchReplaysPayload(t *testing.T) {
	raw, _ := os.ReadFile("../../testdata/nfce_1.html")
	var gotP string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotP = r.URL.Query().Get("p")
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 100, MaxRetries: 1})
	key, _ := ParseAccessKey(senateKey)
	v2 := senateKey + "|2|1|4|af1234"

	body, err := src.Fetch(context.Background(), key, v2)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotP != v2 {
		t.Errorf("portal received p=%q, want the exact payload %q", gotP, v2)
	}
	if !pageHasDANFE(body) {
		t.Errorf("fetched body is not a DANFE")
	}
}

func TestHTTPSourceFetchCaptcha(t *testing.T) {
	raw, _ := os.ReadFile("../../testdata/captcha.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 100, MaxRetries: 1})
	key, _ := ParseAccessKey(senateKey)

	_, err := src.Fetch(context.Background(), key, "")
	var capErr *CaptchaError
	if !errors.As(err, &capErr) {
		t.Fatalf("fetch error = %v, want *CaptchaError", err)
	}
	if capErr.ImageURL == "" {
		t.Errorf("captcha error carries no image URL")
	}
	if !strings.Contains(capErr.ImageURL, "Captcha") {
		t.Errorf("image URL = %q", capErr.ImageURL)
	}

	if n := src.challenges.Count(); n != 1 {
		t.Fatalf("challenges registered = %d, want 1", n)
	}
	ch, err := src.challenges.Get(capErr.ChallengeID)
	if err != nil {
		t.Fatalf("get challenge: %v", err)
	}
	if ch.FormAction == "" {
		t.Errorf("challenge captured no form action")
	}
	if ch.Fields["__VIEWSTATE"] == "" || ch.Fields["__EVENTVALIDATION"] == "" {
		t.Errorf("challenge captured no viewstate fields: %v", ch.Fields)
	}
	// Relative URLs are absolutized against the fetch base.
	if !strings.HasPrefix(ch.ImageURL, srv.URL) || !strings.Contains(ch.ImageURL, "DownloadCaptcha") {
		t.Errorf("absolute image URL = %q", ch.ImageURL)
	}
	if !strings.HasPrefix(ch.FormAction, srv.URL) {
		t.Errorf("absolute form action = %q", ch.FormAction)
	}
	if ch.Payload != "" || ch.AccessKey != senateKey {
		t.Errorf("challenge context = payload %q key %q", ch.Payload, ch.AccessKey)
	}
}

func TestHTTPSourceFetchNotFound(t *testing.T) {
	raw, _ := os.ReadFile("../../testdata/notfound.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 100, MaxRetries: 1})
	key, _ := ParseAccessKey(senateKey)

	_, err := src.Fetch(context.Background(), key, "")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("fetch error = %v, want ErrKeyNotFound", err)
	}
}

func TestHTTPSourceFetchAntiBotRejectionStopsWithoutRetry(t *testing.T) {
	var calls int
	rejected := `<html><head><title>Request Rejected</title></head><body>` +
		`The requested URL was rejected. Please consult with your administrator.` +
		`<br><br>Your support ID is: <10138580085970928105></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(rejected))
	}))
	defer srv.Close()

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 3,
		Cooldown:   time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)

	_, err = src.Fetch(context.Background(), key, "")
	var antiBotErr *AntiBotError
	if !errors.As(err, &antiBotErr) {
		t.Fatalf("fetch error = %v, want *AntiBotError", err)
	}
	if !errors.Is(err, ErrAntiBotRejection) {
		t.Fatalf("fetch error = %v, want ErrAntiBotRejection", err)
	}
	if antiBotErr.StatusCode != http.StatusOK ||
		antiBotErr.SupportID != "10138580085970928105" {
		t.Fatalf("anti-bot error = %+v", antiBotErr)
	}
	if calls != 1 {
		t.Fatalf("server calls = %d, want 1 (no immediate retry)", calls)
	}
}

func TestHTTPSourceFetchAntiBotCooldownAppliesToNextRequest(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	rejected := []byte(`<html><body>Request Rejected. Support ID: 12345</body></html>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write(rejected)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	const cooldown = time.Hour
	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 1,
		Cooldown:   cooldown,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)
	if _, err := src.Fetch(context.Background(), key, ""); !errors.Is(err, ErrAntiBotRejection) {
		t.Fatalf("first fetch error = %v, want anti-bot rejection", err)
	}

	// A fetch during the cooldown must fail fast instead of sleeping through it
	// (the server write timeout is shorter than the default cooldown).
	start := time.Now()
	if _, err := src.Fetch(context.Background(), key, ""); !errors.Is(err, ErrPortalCoolingDown) {
		t.Fatalf("second fetch error = %v, want ErrPortalCoolingDown", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("second fetch took %v, want an immediate cooldown error", elapsed)
	}
	if calls != 1 {
		t.Fatalf("server calls = %d, want 1 (no fetch during cooldown)", calls)
	}
}

func TestRateLimiterCooldownExpires(t *testing.T) {
	rl := NewPortalLimiter(1000, 0)
	rl.Cooldown(20 * time.Millisecond)

	if err := rl.Wait(context.Background()); !errors.Is(err, ErrPortalCoolingDown) {
		t.Fatalf("Wait during cooldown = %v, want ErrPortalCoolingDown", err)
	}

	time.Sleep(25 * time.Millisecond)
	if err := rl.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after cooldown = %v, want nil", err)
	}
}

func TestHTTPSourceRetriesThenFailsUpstream(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 2})
	key, _ := ParseAccessKey(senateKey)

	_, err := src.Fetch(context.Background(), key, "")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("fetch error = %v, want ErrUpstream", err)
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2 (initial + 1 retry)", calls)
	}
}

func TestHTTPSourceRetriesThenSucceeds(t *testing.T) {
	var calls int
	raw, _ := os.ReadFile("../../testdata/nfce_2.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 3})
	key, _ := ParseAccessKey(senateKey)

	body, err := src.Fetch(context.Background(), key, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !pageHasDANFE(body) {
		t.Errorf("expected a DANFE body")
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2", calls)
	}
}

func TestHTTPSource429RetryAfterFailsFast(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "1")
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)

	start := time.Now()
	_, err = src.Fetch(context.Background(), key, "")
	var cooldown *PortalCooldownError
	if !errors.As(err, &cooldown) {
		t.Fatalf("fetch error = %v, want *PortalCooldownError", err)
	}
	if cooldown.RetryAfter < 900*time.Millisecond {
		t.Errorf("retry after = %v, want the Retry-After header value", cooldown.RetryAfter)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("fetch took %v, want an immediate cooldown error", elapsed)
	}
	if calls != 1 {
		t.Fatalf("server calls = %d, want 1 (no retry into cooldown)", calls)
	}
	if until := src.limit.CooldownUntil(); !until.After(time.Now()) {
		t.Errorf("cooldown = %v, want an active cooldown", until)
	}
}

func TestHTTPSourceContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 3})
	key, _ := ParseAccessKey(senateKey)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if _, err := src.Fetch(ctx, key, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fetch error = %v, want deadline exceeded", err)
	}
}

func TestHTTPSourceCapsRetryBurst(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	src, _ := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 2})
	if src.retries != 2 {
		t.Errorf("retries = %d, want 2", src.retries)
	}
	key, _ := ParseAccessKey(senateKey)
	_, _ = src.Fetch(context.Background(), key, "")
	if calls != 2 {
		t.Errorf("server calls = %d, want 2", calls)
	}
}

func TestHTTPSourceBackoffKnobs(t *testing.T) {
	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:     "https://portal.example",
		RPS:         100,
		MaxRetries:  5,
		Timeout:     5 * time.Second,
		BackoffBase: 100 * time.Millisecond,
		BackoffCap:  300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.client.Timeout != 5*time.Second {
		t.Errorf("client timeout = %v, want 5s", src.client.Timeout)
	}
	if src.backoffBase != 100*time.Millisecond || src.backoffCap != 300*time.Millisecond {
		t.Errorf("backoff = %v/%v", src.backoffBase, src.backoffCap)
	}
	// Attempt 3 would need 400ms; the 300ms cap clamps it, jitter keeps it >= cap.
	if d := src.backoff(3); d < 300*time.Millisecond || d > 375*time.Millisecond {
		t.Errorf("backoff(3) = %v, want clamped to ~300ms", d)
	}
}

func TestRateLimiterPaces(t *testing.T) {
	rl := NewPortalLimiter(10, 0) // 100ms between tokens
	ctx := context.Background()

	start := time.Now()
	if err := rl.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rl.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 95*time.Millisecond {
		t.Errorf("second Wait returned after %v, want >= ~100ms", elapsed)
	}
}

func TestRateLimiterCooldownFailsFast(t *testing.T) {
	rl := NewPortalLimiter(1000, 0)
	rl.Cooldown(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := rl.Wait(ctx)
	if !errors.Is(err, ErrPortalCoolingDown) {
		t.Fatalf("Wait error = %v, want ErrPortalCoolingDown", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("Wait took %v, want an immediate cooldown error", elapsed)
	}
}

func TestRateLimiterBusyOnDeadline(t *testing.T) {
	rl := NewPortalLimiter(1, 0) // one token per second
	if err := rl.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := rl.Wait(ctx)
	var busy *PortalBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("Wait error = %v, want *PortalBusyError", err)
	}
	if busy.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want a positive delay", busy.RetryAfter)
	}
}

func TestAntiBotRejectionVariants(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		rejected  bool
		supportID string
	}{
		{
			name:      "observed response",
			body:      "Request Rejected. Your support ID is: <12345>",
			rejected:  true,
			supportID: "12345",
		},
		{
			name:      "case and whitespace variant",
			body:      "THE REQUESTED URL WAS REJECTED. Support ID:  abc-123",
			rejected:  true,
			supportID: "abc-123",
		},
		{
			name:     "ordinary malformed page",
			body:     "Consulta Resumida NFC-e is temporarily unavailable",
			rejected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, got := antiBotRejection([]byte(tt.body))
			if got != tt.rejected || gotID != tt.supportID {
				t.Fatalf("antiBotRejection() = %q, %v; want %q, %v",
					gotID, got, tt.supportID, tt.rejected)
			}
		})
	}
}

func TestParseRetryAfterDateUsesProvidedNow(t *testing.T) {
	now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	header := http.Header{"Retry-After": []string{"Wed, 03 Nov 2026 12:00:05 GMT"}}
	delay, ok := parseRetryAfter(header, now)
	if !ok || delay != 5*time.Second {
		t.Fatalf("parseRetryAfter() = %v, %v; want 5s, true", delay, ok)
	}
}

func TestHTTPSourceFetchHeaders(t *testing.T) { // Fixed scope for srv
	var capturedUA string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")

		// Verify headers are set correctly as per Phase 3 requirements
		if r.Method != "GET" {
			t.Errorf("expected method GET, got %s", r.Method)
		}

		if r.Header.Get("Accept") != "text/html,application/xhtml+xml" {
			t.Errorf("unexpected Accept header: %q", r.Header.Get("Accept"))
		}

		if r.Header.Get("Accept-Language") != "pt-BR,pt;q=0.9" {
			t.Errorf("unexpected Accept-Language header: %q", r.Header.Get("Accept-Language"))
		}

		// Check if Host is correct (it should be a local address)
		if !strings.Contains(r.Host, "127.0.0.1") && !strings.Contains(r.Host, "localhost") {
			// httptest server usually uses localhost/127.0.0.1
			t.Errorf("expected host to be local, got %s", r.Host)
		}

		_, _ = w.Write([]byte(`<html><body style="height:100px;width:100px;">Consulta Resumida NFC-e content here... This is a dummy DANFE page for testing purposes and it should be longer than one hundred characters to satisfy the requirements of the fetcher logic.</h1></body></html>`))
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	customUA := "Test Agent/1.0"
	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        100,
		MaxRetries: 1,
		UserAgent:  customUA,
	})
	if err != nil {
		t.Fatal(err)
	}

	key, _ := ParseAccessKey(senateKey)
	_, err = src.Fetch(context.Background(), key, "")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	if capturedUA != customUA {
		t.Errorf("expected user agent %q, got %q", customUA, capturedUA)
	}
}

func TestHTTPSourceDefaultsToSingleAttempt(t *testing.T) {
	src, err := NewHTTPSource(HTTPSourceOptions{BaseURL: "https://portal.example"})
	if err != nil {
		t.Fatal(err)
	}
	if src.retries != 1 {
		t.Errorf("default retries = %d, want 1", src.retries)
	}
	// RPS defaults to 1 => 1s, and no MinInterval override.
	if src.limit.interval != time.Second {
		t.Errorf("default interval = %v, want 1s", src.limit.interval)
	}
}

func TestHTTPSourceMinIntervalOverridesRPS(t *testing.T) {
	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:     "https://portal.example",
		RPS:         1000,
		MinInterval: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.limit.interval != 10*time.Second {
		t.Errorf("interval = %v, want MinInterval 10s", src.limit.interval)
	}
}

func TestRateLimiterHonorsMinInterval(t *testing.T) {
	// RPS alone would allow a token every 1ms; MinInterval must win.
	rl := NewPortalLimiter(1000, 100*time.Millisecond)
	ctx := context.Background()

	start := time.Now()
	if err := rl.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rl.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 95*time.Millisecond {
		t.Errorf("second Wait returned after %v, want >= ~100ms (MinInterval)", elapsed)
	}
}

func TestHTTPSourceEdgeDropStopsWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("response writer is not a Hijacker")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		// Close without writing a response: the client sees EOF/reset, which
		// is how the SEFAZ bot-defense edge rejects a challenge-less request.
		_ = conn.Close()
	}))
	defer srv.Close()

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 3,
		Cooldown:   50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)

	_, err = src.Fetch(context.Background(), key, "")
	if !errors.Is(err, ErrAntiBotRejection) {
		t.Fatalf("fetch error = %v, want ErrAntiBotRejection", err)
	}
	var antiBotErr *AntiBotError
	if !errors.As(err, &antiBotErr) {
		t.Fatalf("fetch error = %v, want *AntiBotError", err)
	}
	if antiBotErr.StatusCode != 0 || antiBotErr.Reason == "" {
		t.Errorf("edge-drop error = %+v, want no status and a reason", antiBotErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("server calls = %d, want 1 (no retry on an edge drop)", calls.Load())
	}
	if until := src.limit.CooldownUntil(); !until.After(time.Now()) {
		t.Errorf("cooldown = %v, want an active cooldown", until)
	}
}

func TestIsEdgeDrop(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"reset", errors.New("read tcp 192.0.2.1:443: read: connection reset by peer"), true},
		{"goaway", errors.New("http2: server sent GOAWAY and closed the connection"), true},
		{"broken pipe", errors.New("write: broken pipe"), true},
		{"connection refused", errors.New("dial tcp: connect: connection refused"), false},
		{"tls", errors.New("tls: handshake failure"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEdgeDrop(tt.err); got != tt.want {
				t.Errorf("isEdgeDrop(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestHTTPSourceSendsDiagnosticCookie(t *testing.T) {
	var gotCookie string
	raw, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 1,
		Cookie:     "ASP.NET_SessionId=abc; TSPD_101=def",
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)
	if _, err := src.Fetch(context.Background(), key, ""); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotCookie != "ASP.NET_SessionId=abc; TSPD_101=def" {
		t.Errorf("Cookie header = %q, want the supplied diagnostic value", gotCookie)
	}
}

func TestHTTPSourceLogsCookieNamesNotValues(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "super-secret-cookie-value"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:  "ASP.NET_SessionId",
			Value: secret,
			Path:  "/",
		})
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 1,
		Logger:     logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)

	for i := 0; i < 2; i++ {
		if _, err := src.Fetch(context.Background(), key, ""); err != nil {
			t.Fatalf("fetch %d: %v", i+1, err)
		}
	}

	out := logs.String()
	if !strings.Contains(out, "ASP.NET_SessionId") {
		t.Errorf("cookie names not logged: %s", out)
	}
	if strings.Contains(out, secret) {
		t.Errorf("cookie value leaked into logs: %s", out)
	}
}

func TestHTTPSourceCaptchaWithoutCookieJarDoesNotPanic(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/captcha.html")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	// A custom client with no jar used to panic when captureChallenge read
	// s.client.Jar.Cookies.
	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 1,
		HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)

	_, err = src.Fetch(context.Background(), key, "")
	var capErr *CaptchaError
	if !errors.As(err, &capErr) {
		t.Fatalf("fetch error = %v, want *CaptchaError", err)
	}
	if capErr.ChallengeID == "" {
		t.Errorf("challenge was not registered")
	}
}

func TestHTTPSourceJarSendsCookieOnNextRequest(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.Header.Get("Cookie"))
		http.SetCookie(w, &http.Cookie{Name: "ASP.NET_SessionId", Value: "abc", Path: "/"})
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	src, err := NewHTTPSource(HTTPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)
	for i := 0; i < 2; i++ {
		if _, err := src.Fetch(context.Background(), key, ""); err != nil {
			t.Fatalf("fetch %d: %v", i+1, err)
		}
	}

	if len(sent) != 2 {
		t.Fatalf("requests = %d, want 2", len(sent))
	}
	if sent[0] != "" {
		t.Errorf("first request Cookie = %q, want empty", sent[0])
	}
	if !strings.Contains(sent[1], "ASP.NET_SessionId=abc") {
		t.Errorf("second request Cookie = %q, want the jar cookie", sent[1])
	}
}

func TestHTTPSourceDiagnosticCookieDoesNotDuplicateJarCookies(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.Header.Get("Cookie"))
		http.SetCookie(w, &http.Cookie{Name: "TSPD_101", Value: "fresh", Path: "/"})
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 1,
		Cookie:     "ASP.NET_SessionId=abc; TSPD_101=def",
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)
	for i := 0; i < 2; i++ {
		if _, err := src.Fetch(context.Background(), key, ""); err != nil {
			t.Fatalf("fetch %d: %v", i+1, err)
		}
	}

	if len(sent) != 2 {
		t.Fatalf("requests = %d, want 2", len(sent))
	}
	for i, header := range sent {
		if got := strings.Count(header, "ASP.NET_SessionId="); got != 1 {
			t.Errorf("request %d: ASP.NET_SessionId appears %d times in %q", i+1, got, header)
		}
		if got := strings.Count(header, "TSPD_101="); got != 1 {
			t.Errorf("request %d: TSPD_101 appears %d times in %q", i+1, got, header)
		}
	}
}

func TestPortalLimiterSharedBetweenFetcherAndSolver(t *testing.T) {
	var imageCalls atomic.Int32
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		imageCalls.Add(1)
		_, _ = w.Write([]byte("png"))
	}))
	defer imgSrv.Close()

	limiter := NewPortalLimiter(1000, 0)
	src, err := NewHTTPSource(HTTPSourceOptions{BaseURL: "https://portal.example", Limiter: limiter})
	if err != nil {
		t.Fatal(err)
	}
	solver := NewHTTPChallengeSolver(HTTPChallengeSolverOptions{
		Limiter:    limiter,
		HTTPClient: &http.Client{},
	})
	if src.limit != solver.limit {
		t.Fatalf("fetcher and solver do not share the limiter")
	}

	limiter.Cooldown(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err = solver.Image(ctx, &Challenge{ImageURL: imgSrv.URL})
	if !errors.Is(err, ErrPortalCoolingDown) {
		t.Fatalf("solver.Image error = %v, want ErrPortalCoolingDown", err)
	}
	if imageCalls.Load() != 0 {
		t.Errorf("image requests = %d, want 0 during shared cooldown", imageCalls.Load())
	}
}
