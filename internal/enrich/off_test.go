package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const testGTIN = "07896008038717" // 7896008038717 padded; valid check digit

// fixtureGTINs are known-valid 789 codes that resolve live on
// br.openfoodfacts.org (verified Oct/2026) used for the env-gated live test.
var liveGTINs = []string{"7891000412855", "7894900701517"}

func offHandler(t *testing.T, hits *int) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("missing User-Agent header")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/api/v2/product/"+testGTIN {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("fields"); got != "product_name,brands,quantity" {
			t.Errorf("fields = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": 1,
			"product": map[string]string{
				"product_name": "Pão de Forma Integral 500g",
				"brands":       "Pullman",
				"quantity":     "500 g",
			},
		})
	})
}

func TestOFFLookupHit(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(offHandler(t, &hits))
	defer srv.Close()

	c := NewOFFClient(WithBaseURL(srv.URL))
	p, err := c.Lookup(context.Background(), testGTIN)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if p.Name != "Pão de Forma Integral 500g" || p.Brand != "Pullman" {
		t.Errorf("product = %+v", p)
	}
	if p.Unit != "GR" {
		t.Errorf("unit = %q, want GR (500 g)", p.Unit)
	}

	// Second lookup of the same GTIN must hit the cache, not the network.
	if _, err := c.Lookup(context.Background(), testGTIN); err != nil {
		t.Fatalf("cached lookup: %v", err)
	}
	if hits != 1 {
		t.Errorf("network hits = %d, want 1 (cached)", hits)
	}

	stats := c.CacheStats()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Errorf("cache stats = %+v, want 1 hit / 1 miss", stats)
	}
}

func TestOFFLookupNotFound(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 0})
	}))
	defer srv.Close()

	c := NewOFFClient(WithBaseURL(srv.URL))
	_, err := c.Lookup(context.Background(), testGTIN)
	if !errors.Is(err, ErrProductNotFound) {
		t.Fatalf("err = %v, want ErrProductNotFound", err)
	}
	if hits != 1 {
		t.Errorf("network hits = %d, want 1", hits)
	}
	// Negative results are cached too: no second hit.
	_, _ = c.Lookup(context.Background(), testGTIN)
	if hits != 1 {
		t.Errorf("network hits after negative = %d, want 1", hits)
	}
}

func TestOFFLookupDegradesOffline(t *testing.T) {
	// A dead server is a transport error, not ErrProductNotFound: the caller
	// falls back to gtin_new without failing the ingestion.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable

	c := NewOFFClient(WithBaseURL(url))
	_, err := c.Lookup(context.Background(), testGTIN)
	if err == nil {
		t.Fatal("lookup on dead server: want transport error")
	}
	if errors.Is(err, ErrProductNotFound) {
		t.Errorf("dead server must degrade as transport error, got %v", err)
	}
}

func TestOFFLookupTimeout(t *testing.T) {
	// The default 5s timeout must be honored and surfaced as an error, not a
	// hang. A handler that sleeps past the client deadline proves it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewOFFClient(WithBaseURL(srv.URL))
	start := time.Now()
	_, err := c.Lookup(context.Background(), testGTIN)
	if err == nil {
		t.Fatal("lookup over deadline: want error")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("lookup took %s; timeout not respected", time.Since(start))
	}
}

func TestOFFLookupHTTP5xxIsTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewOFFClient(WithBaseURL(srv.URL))
	_, err := c.Lookup(context.Background(), testGTIN)
	if err == nil || errors.Is(err, ErrProductNotFound) {
		t.Errorf("5xx err = %v, want transport error", err)
	}
}

// TestOFFLookupLive hits the real br.openfoodfacts.org API. It is opt-in via
// MEUPRECO_LIVE_OFF=1 so ordinary test runs stay hermetic and offline-safe.
func TestOFFLookupLive(t *testing.T) {
	if os.Getenv("MEUPRECO_LIVE_OFF") != "1" {
		t.Skip("set MEUPRECO_LIVE_OFF=1 to run the live OFF test")
	}
	c := NewOFFClient()
	for _, gtin := range liveGTINs {
		p, err := c.Lookup(context.Background(), gtin)
		if err != nil {
			t.Errorf("live lookup %s: %v", gtin, err)
			continue
		}
		if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Brand) == "" {
			t.Errorf("live lookup %s: empty name/brand: %+v", gtin, p)
		}
		t.Logf("OFF: %s %s (%s) unit=%q", gtin, p.Brand, p.Name, p.Unit)
	}
}

func TestInferUnit(t *testing.T) {
	tests := map[string]string{
		"500 g":       "GR",
		"1 kg":        "KG",
		"3 x 200 ml":  "ML",
		"2 L":         "LT",
		"350g":        "GR",
		"1kg":         "KG",
		"12 unidades": "UN",
		"":            "",
	}
	for in, want := range tests {
		if got := inferUnit(in); got != want {
			t.Errorf("inferUnit(%q) = %q, want %q", in, got, want)
		}
	}
}
