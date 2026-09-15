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

func TestSPSourceFetchOK(t *testing.T) {
	srv := serveFixture(t, "nfce_1.html")
	defer srv.Close()

	src, err := NewSPSource(SPSourceOptions{
		BaseURL:    srv.URL,
		RPS:        100,
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	key, _ := ParseAccessKey(senateKey)
	body, err := src.Fetch(context.Background(), key)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !pageHasDANFE(body) {
		t.Errorf("fetched body is not a DANFE")
	}
}

func TestSPSourceFetchCaptcha(t *testing.T) {
	raw, _ := os.ReadFile("../../testdata/captcha.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	src, _ := NewSPSource(SPSourceOptions{BaseURL: srv.URL, RPS: 100, MaxRetries: 1})
	key, _ := ParseAccessKey(senateKey)

	_, err := src.Fetch(context.Background(), key)
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
	if len(src.challenges.next) != 1 {
		t.Fatalf("challenges registered = %d, want 1", len(src.challenges.next))
	}
	var storedID string
	for id := range src.challenges.next {
		storedID = id
	}
	if capErr.ChallengeID != storedID {
		t.Errorf("challenge id = %q, want registered id %q", capErr.ChallengeID, storedID)
	}
}

func TestSPSourceFetchNotFound(t *testing.T) {
	raw, _ := os.ReadFile("../../testdata/notfound.html")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	src, _ := NewSPSource(SPSourceOptions{BaseURL: srv.URL, RPS: 100, MaxRetries: 1})
	key, _ := ParseAccessKey(senateKey)

	_, err := src.Fetch(context.Background(), key)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("fetch error = %v, want ErrKeyNotFound", err)
	}
}

func TestSPSourceRetriesThenFailsUpstream(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	src, _ := NewSPSource(SPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 2})
	key, _ := ParseAccessKey(senateKey)

	_, err := src.Fetch(context.Background(), key)
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("fetch error = %v, want ErrUpstream", err)
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2 (initial + 1 retry)", calls)
	}
}

func TestSPSourceRetriesThenSucceeds(t *testing.T) {
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

	src, _ := NewSPSource(SPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 3})
	key, _ := ParseAccessKey(senateKey)

	body, err := src.Fetch(context.Background(), key)
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

func TestSPSourceContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	src, _ := NewSPSource(SPSourceOptions{BaseURL: srv.URL, RPS: 1000, MaxRetries: 3})
	key, _ := ParseAccessKey(senateKey)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if _, err := src.Fetch(ctx, key); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fetch error = %v, want deadline exceeded", err)
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
