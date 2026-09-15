package nfce

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestLiveFetchSenateKey hits the real São Paulo portal with the publicly
// disclosed Senate CEAPS NFC-e key. It only runs when MEUPRECO_LIVE=1 so CI
// stays hermetically offline. A captcha challenge is an acceptable
// environmental outcome (the portal throttles per IP) and just skips the test.
func TestLiveFetchSenateKey(t *testing.T) {
	if os.Getenv("MEUPRECO_LIVE") != "1" {
		t.Skip("set MEUPRECO_LIVE=1 to run the live SEFAZ fetch")
	}

	src, err := NewHTTPSource(HTTPSourceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parser := &HTMLParser{}

	key, err := ParseAccessKey(senateKey)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := src.Fetch(context.Background(), key, "")
	if err != nil {
		var capErr *CaptchaError
		if errors.As(err, &capErr) {
			t.Skipf("portal answered a captcha challenge (%s): %v", capErr.ChallengeID, capErr)
		}
		t.Fatalf("fetch: %v", err)
	}

	n, err := parser.Parse(context.Background(), key, raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(n.Items) == 0 {
		t.Errorf("live receipt parsed with zero items")
	}
	if n.Supermarket.LegalName == "" || n.Supermarket.CNPJ == "" {
		t.Errorf("live receipt missing emitter data: %+v", n.Supermarket)
	}
	if n.Number == "" || n.Protocol == "" {
		t.Errorf("live receipt missing metadata: %+v", n)
	}
	t.Logf("live receipt: %s — %s (%d items, %s)", n.Supermarket.LegalName, n.Supermarket.CNPJ, len(n.Items), n.PaidValue)
}

// crossStateSamples are real, publicly disclosed NFC-e keys from the Instituto
// OPS audit of Senate CEAPS spending
// (https://institutoops.org.br/wp-content/uploads/2021/10/relatorio-final-otf4.pdf),
// published under transparency law. They give the national portals legitimate
// requests to exercise with; none of these states is supported yet.
var crossStateSamples = []struct {
	uf  string
	key string
}{
	{"12", "12210210209732000140650650000837491000851994"}, // AC
	{"21", "21201007801429000127650010002724531476531104"}, // MA
	{"22", "22200702384264000383650020001289121770839990"}, // PI
	{"26", "26190821280350000129650060000584341000603570"}, // PE
	{"27", "27190912975538000109650010002039711001600448"}, // AL
}

// TestLiveProbeCrossState documents how the unsupported states' portals behave
// from this installation's perspective (single polite GET per sample). As a
// state becomes supported its entry moves to a strict assertion. Runs only
// with MEUPRECO_LIVE=1.
func TestLiveProbeCrossState(t *testing.T) {
	if os.Getenv("MEUPRECO_LIVE") != "1" {
		t.Skip("set MEUPRECO_LIVE=1 to probe the national portals")
	}

	for _, s := range crossStateSamples {
		cfg, ok := LookupUF(s.uf)
		if !ok {
			t.Fatalf("UF %s missing from registry", s.uf)
		}

		src, err := NewHTTPSource(HTTPSourceOptions{BaseURL: cfg.QRBaseURL, RPS: 1, MaxRetries: 1})
		if err != nil {
			t.Fatalf("UF %s: %v", s.uf, err)
		}
		key, err := ParseAccessKey(s.key)
		if err != nil {
			t.Fatalf("UF %s: %v", s.uf, err)
		}

		raw, err := src.Fetch(context.Background(), key, "")
		switch {
		case err == nil && pageHasDANFE(raw):
			t.Errorf("UF %s: portal returned a server-side DANFE for an unsupported state — revisit the registry", s.uf)
		case err == nil:
			t.Logf("UF %s: fetched an unidentified page (no DANFE markers)", s.uf)
		default:
			var capErr *CaptchaError
			if errors.As(err, &capErr) {
				t.Logf("UF %s: portal answered a captcha challenge", s.uf)
				continue
			}
			t.Logf("UF %s: %v", s.uf, err)
		}
	}
}
