package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"meupreco/internal/config"
	"meupreco/internal/nfce"
)

// TestNewNfceResolverSharesPortalState proves the production wiring: a
// challenge registered by the fetcher is visible to the captcha support, and
// both use the same pacing/cooldown limiter. Before this, newNfceResolver and
// newCaptchaSupport each built their own store, so a minted challenge could
// never be solved.
func TestNewNfceResolverSharesPortalState(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/captcha.html")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	cfg := config.Config{
		SefazBaseURL:     srv.URL,
		SEFAZFetchRPS:    1000,
		SEFAZMaxRetries:  1,
		SEFAZTimeout:     2 * time.Second,
		SEFAZCooldown:    50 * time.Millisecond,
		SEFAZBackoffBase: time.Millisecond,
		SEFAZBackoffCap:  time.Millisecond,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	portal := newPortalState(cfg, log)

	res := newNfceResolver(cfg, log, portal)
	key, err := nfce.ParseAccessKey(senateKey)
	if err != nil {
		t.Fatal(err)
	}

	_, err = res.Resolve(context.Background(), key, "")
	var capErr *nfce.CaptchaError
	if !errors.As(err, &capErr) {
		t.Fatalf("resolve error = %v, want *nfce.CaptchaError", err)
	}

	if _, err := portal.challenges.Get(capErr.ChallengeID); err != nil {
		t.Errorf("fetcher challenge not in the shared store: %v", err)
	}

	support := newCaptchaSupport(cfg, log, portal)
	if support.Store != portal.challenges {
		t.Errorf("captcha support does not use the shared challenge store")
	}
	if support.Solver == nil {
		t.Errorf("captcha support has no solver")
	}
}

// TestNewNfceResolverSendsConfiguredUserAgent proves the SEFAZ_USER_AGENT
// configuration (and the ASCII default when unset) reaches the actual portal
// request, not just the config struct.
func TestNewNfceResolverSendsConfiguredUserAgent(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/nfce_1.html")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		ua   string
		want string
	}{
		{name: "configured", ua: "MeuPreco/wiring-test", want: "MeuPreco/wiring-test"},
		{name: "default", ua: "", want: "MeuPreco/0.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uaCh := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				uaCh <- r.UserAgent()
				_, _ = w.Write(raw)
			}))
			defer srv.Close()

			cfg := config.Config{
				SefazBaseURL:     srv.URL,
				SEFAZFetchRPS:    1000,
				SEFAZMaxRetries:  1,
				SEFAZTimeout:     2 * time.Second,
				SEFAZCooldown:    50 * time.Millisecond,
				SEFAZBackoffBase: time.Millisecond,
				SEFAZBackoffCap:  time.Millisecond,
				SefazUserAgent:   tc.ua,
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			res := newNfceResolver(cfg, log, newPortalState(cfg, log))

			key, err := nfce.ParseAccessKey(senateKey)
			if err != nil {
				t.Fatal(err)
			}
			// The parser outcome is irrelevant here: only the request header
			// is under test.
			_, _ = res.Resolve(context.Background(), key, "")

			select {
			case got := <-uaCh:
				if got != tc.want {
					t.Errorf("portal User-Agent = %q, want %q", got, tc.want)
				}
			default:
				t.Fatal("portal received no request")
			}
		})
	}
}
