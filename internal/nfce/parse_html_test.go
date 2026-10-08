package nfce

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return raw
}

func mustDec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return d
}

func TestParseGroceryReceipt(t *testing.T) {
	key, err := ParseAccessKey(senateKey)
	if err != nil {
		t.Fatal(err)
	}
	parser := &HTMLParser{}
	n, err := parser.Parse(context.Background(), key, fixture(t, "nfce_1.html"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if n.Supermarket.LegalName != "MERCADO BOM PRECO LTDA" {
		t.Errorf("legal name = %q", n.Supermarket.LegalName)
	}
	if n.Supermarket.CNPJ != "52323688000144" {
		t.Errorf("CNPJ = %q", n.Supermarket.CNPJ)
	}
	if !strings.Contains(n.Supermarket.Address, "SAO PAULO") {
		t.Errorf("address = %q", n.Supermarket.Address)
	}

	if len(n.Items) != 5 {
		t.Fatalf("items = %d, want 5", len(n.Items))
	}
	wantDescriptions := []string{"LIMAO TAHITI KG", "ARROZ BRANCO TIPO 1 5KG", "FEIJAO CARIOCA 1KG", "BANANA PRATA KG", "REFRIGERANTE COLA PET 2L"}
	for i, want := range wantDescriptions {
		if n.Items[i].Description != want {
			t.Errorf("item[%d] description = %q, want %q", i, n.Items[i].Description, want)
		}
	}

	wantCodes := []string{"00000130", "7896008038717", "7891212345613", "00000174", "7896004021522"}
	for i, want := range wantCodes {
		if n.Items[i].Code != want {
			t.Errorf("item[%d] code = %q, want %q", i, n.Items[i].Code, want)
		}
	}

	// GTIN candidates are surfaced raw (validation is Phase 4).
	if n.Items[1].Code != "7896008038717" {
		t.Errorf("GTIN candidate missing on item 2: %q", n.Items[1].Code)
	}
	if n.Items[2].Code != "7891212345613" {
		t.Errorf("GTIN candidate missing on item 3: %q", n.Items[2].Code)
	}

	// Weight line: 0,815 kg @ 5,9877.
	if !n.Items[0].Qty.Equal(mustDec(t, "0.815")) {
		t.Errorf("item[0] qty = %s", n.Items[0].Qty)
	}
	if n.Items[0].Unit != "KG" {
		t.Errorf("item[0] unit = %q", n.Items[0].Unit)
	}
	if !n.Items[0].UnitPrice.Equal(mustDec(t, "5.9877")) {
		t.Errorf("item[0] unit price = %s", n.Items[0].UnitPrice)
	}
	if !n.Items[0].TotalPrice.Equal(mustDec(t, "4.88")) {
		t.Errorf("item[0] total = %s", n.Items[0].TotalPrice)
	}

	// Count lines are 1-based in page order.
	if n.Items[4].Line != 5 {
		t.Errorf("item[4] line = %d", n.Items[4].Line)
	}

	// Totals.
	if !n.Total.Equal(mustDec(t, "59.06")) {
		t.Errorf("total = %s", n.Total)
	}
	if !n.Discount.Equal(mustDec(t, "2.50")) {
		t.Errorf("discount = %s", n.Discount)
	}
	if !n.PaidValue.Equal(mustDec(t, "56.56")) {
		t.Errorf("paid = %s", n.PaidValue)
	}
	if n.PaymentMethod != "Cartão de Crédito" {
		t.Errorf("payment method = %q", n.PaymentMethod)
	}

	// Metadata.
	if n.Number != "257" || n.Series != "002" {
		t.Errorf("number/series = %q/%q", n.Number, n.Series)
	}
	if want := time.Date(2024, time.November, 15, 8, 52, 53, 0, time.UTC); n.IssuedAt == nil || !n.IssuedAt.Equal(want) {
		t.Errorf("issuedAt = %v, want %v", n.IssuedAt, want)
	}
	if n.Protocol != "435261386373123" {
		t.Errorf("protocol = %q", n.Protocol)
	}
	if n.Environment != "Produção" {
		t.Errorf("environment = %q", n.Environment)
	}
	if n.COO != "182510" {
		t.Errorf("COO = %q", n.COO)
	}

	if len(n.Warnings) != 0 {
		t.Errorf("warnings = %v", n.Warnings)
	}
}

func TestParseBakeryReceipt(t *testing.T) {
	key, _ := ParseAccessKey(bakeryKey)
	parser := &HTMLParser{}
	n, err := parser.Parse(context.Background(), key, fixture(t, "nfce_2.html"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(n.Items) != 6 {
		t.Fatalf("items = %d, want 6", len(n.Items))
	}
	if n.Items[1].Code != "7891910000193" || n.Items[4].Code != "7899876543215" {
		t.Errorf("GTIN candidates not surfaced: %q / %q", n.Items[1].Code, n.Items[4].Code)
	}
	if !n.Total.Equal(mustDec(t, "74.62")) || !n.PaidValue.Equal(mustDec(t, "74.62")) {
		t.Errorf("total/paid = %s/%s", n.Total, n.PaidValue)
	}
	if !n.Discount.IsZero() {
		t.Errorf("discount = %s", n.Discount)
	}
	if n.PaymentMethod != "Dinheiro" {
		t.Errorf("payment method = %q", n.PaymentMethod)
	}
	if n.Number != "91" || n.Series != "001" {
		t.Errorf("number/series = %q/%q", n.Number, n.Series)
	}
}

func TestParsePartialReceiptAddsWarnings(t *testing.T) {
	key, _ := ParseAccessKey(senateKey)
	parser := &HTMLParser{}
	n, err := parser.Parse(context.Background(), key, fixture(t, "nfce_partial.html"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// The receipt still parses.
	if len(n.Items) != 5 {
		t.Fatalf("items = %d", len(n.Items))
	}
	// Item 3 lost its unit price; item 4 lost its code.
	if n.Items[2].ParseError == "" || !strings.Contains(n.Items[2].ParseError, "unit_price") {
		t.Errorf("item[2] parseError = %q", n.Items[2].ParseError)
	}
	if n.Items[3].Code != "" {
		t.Errorf("item[3] code = %q, want empty", n.Items[3].Code)
	}
	if len(n.Warnings) == 0 {
		t.Errorf("no receipt-level warnings")
	}
}

func TestParseNonDANFE(t *testing.T) {
	key, _ := ParseAccessKey(senateKey)
	parser := &HTMLParser{}

	for name, raw := range map[string][]byte{
		"captcha":    fixture(t, "captcha.html"),
		"notfound":   fixture(t, "notfound.html"),
		"random txt": []byte("<html><body>nothing here</body></html>"),
	} {
		if _, err := parser.Parse(context.Background(), key, raw); err == nil {
			t.Errorf("%s: expected error for non-DANFE body", name)
		}
	}
}

func TestPageClassification(t *testing.T) {
	type tc struct {
		name    string
		file    string
		captcha bool
		danfe   bool
	}
	cases := []tc{
		{"grocery", "nfce_1.html", false, true},
		{"bakery", "nfce_2.html", false, true},
		{"partial", "nfce_partial.html", false, true},
		{"captcha", "captcha.html", true, false},
		{"notfound", "notfound.html", false, false},
	}
	for _, c := range cases {
		raw := fixture(t, c.file)
		if got := looksLikeCaptcha(raw); got != c.captcha {
			t.Errorf("%s: looksLikeCaptcha = %v", c.name, got)
		}
		if got := pageHasDANFE(raw); got != c.danfe {
			t.Errorf("%s: pageHasDANFE = %v", c.name, got)
		}
	}

	img := extractCaptchaImage(fixture(t, "captcha.html"))
	if !strings.Contains(img, "Captcha") && !strings.Contains(img, "aptcha") {
		t.Errorf("extractCaptchaImage = %q", img)
	}
}

func TestParseBRDecimal(t *testing.T) {
	for in, want := range map[string]string{
		"1.234,56": "1234.56",
		"7,99":     "7.99",
		"5,9877":   "5.9877",
		"1":        "1",
		"0,00":     "0",
	} {
		d, err := parseBRDecimal(in)
		if err != nil {
			t.Errorf("parseBRDecimal(%q): %v", in, err)
			continue
		}
		if d.StringFixed(2) != want && d.String() != want {
			t.Errorf("parseBRDecimal(%q) = %s, want %s", in, d, want)
		}
	}
	if _, err := parseBRDecimal("NaN"); err == nil {
		t.Errorf("parseBRDecimal(NaN): expected error")
	}
	if _, err := parseBRDecimal(""); err == nil {
		t.Errorf("parseBRDecimal(''): expected error")
	}
}
