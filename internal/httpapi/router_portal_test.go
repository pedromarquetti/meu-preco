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
	portal := newPortalState(cfg)

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
