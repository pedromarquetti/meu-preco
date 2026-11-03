package nfce

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	rl := newRateLimiter(10) // 100ms between tokens
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

