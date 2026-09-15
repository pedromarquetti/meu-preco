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

	src, err := NewSPSource(SPSourceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	parser := &HTMLParser{}

	key, err := ParseAccessKey(senateKey)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := src.Fetch(context.Background(), key)
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
