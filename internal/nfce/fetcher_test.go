package nfce

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// roundTripFunc lets a test inject transport-level responses and errors
// without a live server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// errorReader yields no data and the supplied error, simulating a connection
// that dies mid-body.
type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

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

func TestHTTPSourceFetchAntiBotChallengeStopsWithoutRetry(t *testing.T) {
	const secret = "challenge-body-secret"
	challenge := `<html><head><title>Secure Connection</title>` +
		`<script type="text/javascript" src="/TSPD/abc123?type=challenge"></script></head>` +
		`<body><p>BIG-IP</p><noscript>Please enable JavaScript to continue. ` +
		secret + `</noscript></body></html>`

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(challenge))
	}))
	defer srv.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 3,
		Cooldown:   time.Minute,
		Logger:     logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)

	_, err = src.Fetch(context.Background(), key, "")
	var challengeErr *AntiBotChallengeError
	if !errors.As(err, &challengeErr) {
		t.Fatalf("fetch error = %v, want *AntiBotChallengeError", err)
	}
	if !errors.Is(err, ErrAntiBotChallenge) {
		t.Fatalf("fetch error = %v, want ErrAntiBotChallenge", err)
	}
	if challengeErr.StatusCode != http.StatusOK || challengeErr.BodyBytes != len(challenge) {
		t.Errorf("challenge error = %+v, want status 200 and the body size", challengeErr)
	}
	if challengeErr.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want the configured cooldown", challengeErr.RetryAfter)
	}
	if calls.Load() != 1 {
		t.Fatalf("server calls = %d, want 1 (no retry into the challenge)", calls.Load())
	}
	if until := src.limit.CooldownUntil(); !until.After(time.Now()) {
		t.Errorf("cooldown = %v, want an active cooldown", until)
	}

	// A second fetch must fail fast on the shared cooldown, without touching
	// the portal again.
	if _, err := src.Fetch(context.Background(), key, ""); !errors.Is(err, ErrPortalCoolingDown) {
		t.Fatalf("second fetch error = %v, want ErrPortalCoolingDown", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("server calls = %d, want 1 after the cooldown", calls.Load())
	}

	if out := logs.String(); strings.Contains(out, secret) {
		t.Errorf("raw challenge body leaked into logs: %s", out)
	}
}

func TestAntiBotChallengeVariants(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "TSPD script mixed case",
			body: `<html><head><script src="/TsPd/abc?x=1"></script></head><body>wait</body></html>`,
			want: true,
		},
		{
			name: "TSPD cookie",
			body: `<html><body>TSPD_101=abc; TS01234567=def</body></html>`,
			want: true,
		},
		{
			name: "BIG-IP",
			body: `<html><body>Blocked by BIG-IP</body></html>`,
			want: true,
		},
		{
			name: "BIGIP concatenated",
			body: `<html><body>BIGIP appliance</body></html>`,
			want: true,
		},
		{
			name: "F5 networks with newline",
			body: "<html><body>F5\nNetworks challenge</body></html>",
			want: true,
		},
		{
			name: "JavaScript challenge with whitespace",
			body: "<html><body>JavaScript\n\tchallenge</body></html>",
			want: true,
		},
		{
			name: "ordinary malformed page",
			body: "Consulta Resumida NFC-e is temporarily unavailable",
			want: false,
		},
		{
			name: "DANFE with a challenge marker",
			body: `<html><body id="tabResult"><script src="/TSPD/ignored"></script></body></html>`,
			want: false,
		},
		{
			name: "captcha with a challenge marker",
			body: `<html><body><input type="hidden" name="__VIEWSTATE" value="x">` +
				`<img src="captcha.png"><script src="/TSPD/ignored"></script></body></html>`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marker, got := antiBotChallenge([]byte(tt.body))
			if got != tt.want {
				t.Fatalf("antiBotChallenge() = %q, %v; want %v", marker, got, tt.want)
			}
			if got && marker == "" {
				t.Error("matched challenge has an empty marker")
			}
		})
	}
}

func TestHTTPChallengeSolverSolveDetectsAntiBotChallenge(t *testing.T) {
	challenge := `<html><head><script src="/TSPD/solver"></script></head>` +
		`<body>BIG-IP JavaScript challenge</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_, _ = w.Write([]byte(challenge))
	}))
	defer srv.Close()

	solver := NewHTTPChallengeSolver(HTTPChallengeSolverOptions{
		HTTPClient: &http.Client{},
		Limiter:    NewPortalLimiter(1000, 0),
	})
	ch := &Challenge{
		FormAction: srv.URL,
		Fields:     map[string]string{"__VIEWSTATE": "x"},
	}

	_, err := solver.Solve(context.Background(), ch, "1234")
	if !errors.Is(err, ErrAntiBotChallenge) {
		t.Fatalf("solver error = %v, want ErrAntiBotChallenge", err)
	}
	var challengeErr *AntiBotChallengeError
	if !errors.As(err, &challengeErr) {
		t.Fatalf("solver error = %v, want *AntiBotChallengeError", err)
	}
	if challengeErr.BodyBytes == 0 {
		t.Error("challenge error carries no body size")
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
	typedReset := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"reset", errors.New("read tcp 192.0.2.1:443: read: connection reset by peer"), true},
		{"typed reset", typedReset, true},
		{"wrapped typed reset", fmt.Errorf("wrapped: %w", typedReset), true},
		{"goaway", errors.New("http2: server sent GOAWAY and closed the connection"), false},
		{"closed connection", errors.New("use of closed network connection"), false},
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

func TestIsConnectionDrain(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"goaway", errors.New("http2: server sent GOAWAY and closed the connection"), true},
		{"wrapped goaway", fmt.Errorf("wrapped: %w", errors.New("http2: server sent GOAWAY")), true},
		{"closed connection", errors.New("use of closed network connection"), true},
		{"typed closed", net.ErrClosed, true},
		{"reset", errors.New("connection reset by peer"), false},
		{"connection refused", errors.New("dial tcp: connect: connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConnectionDrain(tt.err); got != tt.want {
				t.Errorf("isConnectionDrain(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestHTTPSourceTransportResetStopsWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	reset := fmt.Errorf("read tcp: %w", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET})

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    "https://portal.example",
		RPS:        1000,
		MaxRetries: 3,
		Cooldown:   time.Minute,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, reset
		})},
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
		t.Errorf("reset error = %+v, want no status and a reason", antiBotErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1 (no retry on a reset)", calls.Load())
	}
	if until := src.limit.CooldownUntil(); !until.After(time.Now()) {
		t.Errorf("cooldown = %v, want an active cooldown", until)
	}
}

func TestHTTPSourceGoAwayRetriesAsTransport(t *testing.T) {
	var calls atomic.Int32

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:     "https://portal.example",
		RPS:         1000,
		MaxRetries:  2,
		Cooldown:    time.Minute,
		BackoffBase: time.Millisecond,
		BackoffCap:  time.Millisecond,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("http2: server sent GOAWAY and closed the connection")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)

	_, err = src.Fetch(context.Background(), key, "")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("fetch error = %v, want ErrUpstream", err)
	}
	if errors.Is(err, ErrAntiBotRejection) || errors.Is(err, ErrAntiBotChallenge) {
		t.Fatalf("fetch error = %v, want a transport classification, not anti-bot", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("transport calls = %d, want 2 (one retry on a fresh connection)", calls.Load())
	}
	if until := src.limit.CooldownUntil(); until.After(time.Now()) {
		t.Errorf("cooldown = %v, want no anti-bot cooldown for GOAWAY", until)
	}
}

func TestHTTPSourceTruncatedBodyIsUpstreamNotEdgeDrop(t *testing.T) {
	const partial = `<html><body>partial`
	var calls atomic.Int32

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    "https://portal.example",
		RPS:        1000,
		MaxRetries: 3,
		Cooldown:   time.Minute,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body: io.NopCloser(io.MultiReader(
					strings.NewReader(partial),
					errorReader{err: io.ErrUnexpectedEOF},
				)),
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)

	_, err = src.Fetch(context.Background(), key, "")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("fetch error = %v, want ErrUpstream", err)
	}
	if errors.Is(err, ErrAntiBotRejection) {
		t.Fatalf("fetch error = %v, want a truncated body, not an edge drop", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, fmt.Sprintf("truncated after %d bytes", len(partial))) ||
		!strings.Contains(msg, "status 200") {
		t.Errorf("fetch error = %q, want status and partial-byte diagnostics", msg)
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1 (no retry on a truncated body)", calls.Load())
	}
	if until := src.limit.CooldownUntil(); until.After(time.Now()) {
		t.Errorf("cooldown = %v, want no anti-bot cooldown for a truncated body", until)
	}
}

func TestHTTPSourceHTTP403IsAntiBot(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 3,
		Cooldown:   time.Minute,
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
	if antiBotErr.StatusCode != http.StatusForbidden || antiBotErr.Reason == "" {
		t.Errorf("403 error = %+v, want the status and a reason", antiBotErr)
	}
	if !errors.Is(err, ErrAntiBotRejection) {
		t.Fatalf("fetch error = %v, want ErrAntiBotRejection", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("server calls = %d, want 1 (no retry on an HTTP block)", calls.Load())
	}
	if until := src.limit.CooldownUntil(); !until.After(time.Now()) {
		t.Errorf("cooldown = %v, want an active cooldown", until)
	}
}

func TestHTTPSourceEmpty200IsAntiBot(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        1000,
		MaxRetries: 3,
		Cooldown:   time.Minute,
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
	if antiBotErr.StatusCode != http.StatusOK || antiBotErr.Reason == "" {
		t.Errorf("empty-200 error = %+v, want the status and a reason", antiBotErr)
	}
	if !errors.Is(err, ErrAntiBotRejection) {
		t.Fatalf("fetch error = %v, want ErrAntiBotRejection", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("server calls = %d, want 1 (no retry on a blank block page)", calls.Load())
	}
	if until := src.limit.CooldownUntil(); !until.After(time.Now()) {
		t.Errorf("cooldown = %v, want an active cooldown", until)
	}
}

func TestParseRetryAfterCapsAndRejectsNegative(t *testing.T) {
	for _, value := range []string{"999999999", strings.Repeat("9", 40)} {
		huge := http.Header{"Retry-After": []string{value}}
		delay, ok := parseRetryAfter(huge, time.Now())
		if !ok || delay != maxPortalCooldown {
			t.Errorf("Retry-After %q = %v, %v; want %v, true", value, delay, ok, maxPortalCooldown)
		}
	}

	negative := http.Header{"Retry-After": []string{"-10"}}
	if delay, ok := parseRetryAfter(negative, time.Now()); ok || delay != 0 {
		t.Errorf("negative Retry-After = %v, %v; want 0, false", delay, ok)
	}

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:  "https://portal.example",
		Cooldown: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.cooldown != maxPortalCooldown {
		t.Errorf("configured cooldown = %v, want %v", src.cooldown, maxPortalCooldown)
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

func TestHTTPSourceLogsSafeTransportDiagnostics(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "cookie-value-must-not-leak"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "test-edge")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Via", "1.1 test-proxy")
		w.Header().Set("X-Cache", "HIT")
		http.SetCookie(w, &http.Cookie{Name: "TSPD_101", Value: secret, Path: "/"})
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
		HTTPClient: srv.Client(),
		Cookie:     "ASP.NET_SessionId=" + secret,
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
	for _, want := range []string{
		"portal request diagnostics",
		"portal response diagnostics",
		"connection_established=true",
		"connection_reused=",
		"remote_addr=",
		"tls_version=1.",
		"first_byte_ms=",
		"server=test-edge",
		`content_type="text/html; charset=utf-8"`,
		`via="1.1 test-proxy"`,
		"x_cache=HIT",
		"cookie_count_sent=",
		"cookie_names_sent=",
		"cookie_count_set=1",
		"cookie_names_set=[TSPD_101]",
		"cookie_attrs_set=",
		"challenge_cookie=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, secret) {
		t.Errorf("cookie value leaked into logs:\n%s", out)
	}
}

func TestHTTPSourceNeverLogsFullQRPayload(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	const payloadSecret = "qr-payload-must-not-leak"
	payload := key.String() + "|3|1|" + payloadSecret

	if _, err := src.Fetch(context.Background(), key, payload); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	out := logs.String()
	if strings.Contains(out, payloadSecret) {
		t.Errorf("full QR payload leaked into logs:\n%s", out)
	}
	if strings.Contains(out, "p=") {
		t.Errorf("query string leaked into logs:\n%s", out)
	}
	for _, want := range []string{
		"host=127.0.0.1:",
		"path=/",
		"access_key=" + key.String(),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}
}

func TestHTTPSourceTransportErrorRedactsQRPayload(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	const payloadSecret = "qr-payload-must-not-leak"
	payload := senateKey + "|3|1|" + payloadSecret

	src, err := NewHTTPSource(HTTPSourceOptions{
		BaseURL:    "https://portal.example",
		RPS:        1000,
		MaxRetries: 1,
		Logger:     logger,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("transport boom")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ParseAccessKey(senateKey)

	_, err = src.Fetch(context.Background(), key, payload)
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("fetch error = %v, want ErrUpstream", err)
	}
	if strings.Contains(err.Error(), payloadSecret) {
		t.Errorf("QR payload leaked into error: %v", err)
	}
	if strings.Contains(err.Error(), "p=") {
		t.Errorf("query string leaked into error: %v", err)
	}
	out := logs.String()
	if strings.Contains(out, payloadSecret) || strings.Contains(out, "p=") {
		t.Errorf("QR payload leaked into logs:\n%s", out)
	}
	if !strings.Contains(out, "portal transport failed") {
		t.Errorf("transport failure was not logged safely:\n%s", out)
	}
}

func TestSolverLogsSafeDiagnostics(t *testing.T) {
	const (
		cookieSecret   = "solver-cookie-value-must-not-leak"
		querySecret    = "solver-query-must-not-leak"
		solutionSecret = "solver-answer-must-not-leak"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/image") {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("not-really-a-png"))
			return
		}
		raw, err := os.ReadFile("../../testdata/captcha.html")
		if err != nil {
			t.Errorf("read captcha fixture: %v", err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "ASP.NET_SessionId", Value: cookieSecret, Path: "/"})
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	solver := NewHTTPChallengeSolver(HTTPChallengeSolverOptions{
		HTTPClient: srv.Client(),
		Logger:     logger,
		RPS:        1000,
	})
	ch := &Challenge{
		AccessKey:  senateKey,
		FormAction: srv.URL + "/solve?token=" + querySecret,
		ImageURL:   srv.URL + "/image?token=" + querySecret,
		Fields:     map[string]string{"__VIEWSTATE": querySecret},
		Cookies:    []*http.Cookie{{Name: "ASP.NET_SessionId", Value: cookieSecret}},
	}

	if _, err := solver.Solve(context.Background(), ch, solutionSecret); !errors.Is(err, ErrWrongSolution) {
		t.Fatalf("Solve = %v, want ErrWrongSolution", err)
	}
	if _, ctype, err := solver.Image(context.Background(), ch); err != nil || ctype != "image/png" {
		t.Fatalf("Image = %q, %v; want image/png and no error", ctype, err)
	}

	out := logs.String()
	for _, want := range []string{
		"portal captcha solve request",
		"portal captcha solve response",
		"portal captcha answer rejected",
		"portal captcha image request",
		"portal captcha image response",
		"path=/solve",
		"path=/image",
		"access_key=" + senateKey,
		"cookie_names_sent=[ASP.NET_SessionId]",
		"cookie_names_set=[ASP.NET_SessionId]",
		"content_type=image/png",
		"status=200",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{cookieSecret, querySecret, solutionSecret} {
		if strings.Contains(out, secret) {
			t.Errorf("solver secret %q leaked into logs:\n%s", secret, out)
		}
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
