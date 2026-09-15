package nfce

import (
	"strings"
	"testing"
	"time"
)

// Publicly available test keys:
//   - senateKey is a real, publicly-disclosed NFC-e key from the Brazilian
//     Senate's CEAPS (parlamentary expense) transparency portal — a SP
//     restaurant receipt, published for accountability, no private data.
//   - bakeryKey is a synthetic, structurally valid key (SP, 11/2025, CNPJ
//     11.222.333/0001-81) used to keep a second fixture free of real data.
const (
	senateKey = "35241152323688000144650020000002571115675901"
	bakeryKey = "35251111222333000181650010000000911000013378"
)

func TestParseAccessKeyValid(t *testing.T) {
	for _, raw := range []string{
		senateKey,
		"3524 1152 3236 8800 0144 6500 2000 0002 5711 1567 5901", // masked
		"3524.1152.3236.8800.0144.6500.2000.0002.5711.1567.5901", // dotted
		bakeryKey,
	} {
		k, err := ParseAccessKey(raw)
		if err != nil {
			t.Errorf("ParseAccessKey(%q): %v", raw, err)
			continue
		}
		if k.String() != senateKey && k.String() != bakeryKey {
			t.Errorf("ParseAccessKey(%q) = %q", raw, k.String())
		}
	}
}

func TestParseAccessKeyInvalid(t *testing.T) {
	bad := []string{
		"",
		"123",           // too short
		senateKey[:43],  // 43 digits
		senateKey + "1", // 45 digits
		"3524115232368800014465002000000257111567590X", // digit expected
		"35241152323688000144650020000002571115675902", // wrong check digit
		"55241152323688000144650020000002571115675901", // NF-e model 55
		"99241152323688000144650020000002571115675901", // invalid UF
	}
	for _, raw := range bad {
		if _, err := ParseAccessKey(raw); err == nil {
			t.Errorf("ParseAccessKey(%q): expected error", raw)
		} else if !strings.Contains(err.Error(), "access key") {
			t.Errorf("ParseAccessKey(%q): error %q does not mention access key", raw, err)
		}
	}
}

func TestAccessKeyFields(t *testing.T) {
	k, err := ParseAccessKey(senateKey)
	if err != nil {
		t.Fatal(err)
	}
	if k.UFCode() != "35" {
		t.Errorf("UFCode = %q, want 35", k.UFCode())
	}
	if k.EmitterCNPJ() != "52323688000144" {
		t.Errorf("EmitterCNPJ = %q", k.EmitterCNPJ())
	}
	if k.Series() != "002" {
		t.Errorf("Series = %q, want 002", k.Series())
	}
	if k.Number() != "000000257" {
		t.Errorf("Number = %q, want 000000257", k.Number())
	}
	y, m, err := k.EmissionMonth()
	if err != nil {
		t.Fatal(err)
	}
	if y != 2024 || m != time.November {
		t.Errorf("EmissionMonth = %d/%d, want 2024/11", y, m)
	}
}

func TestAccessKeyBadMonth(t *testing.T) {
	k, _ := ParseAccessKey(senateKey)
	_, _, err := k.EmissionMonth()
	if err != nil {
		t.Fatal(err)
	}
	fake := AccessKey{v: "3524" + "99" + senateKey[6:]}
	if _, _, err := fake.EmissionMonth(); err == nil {
		t.Errorf("EmissionMonth with month 99: expected error")
	}
}

func TestCheckDigitRejectsTampering(t *testing.T) {
	// Change the check digit itself: every other digit means an invalid DV,
	// guaranteed, whatever the 10-digit collision odds would be otherwise.
	tampered := senateKey[:43] + "2"
	if senateKey[43] == '2' {
		tampered = senateKey[:43] + "3"
	}
	if _, err := ParseAccessKey(tampered); err == nil {
		t.Errorf("tampered DV accepted")
	}
}

func TestQRCodeURL(t *testing.T) {
	k, _ := ParseAccessKey(senateKey)
	b := SPURLBuilder{BaseURL: "https://portal.test"}
	want := "https://portal.test?p=" + senateKey + "|3|1"
	if got := b.QRCodeURL(k, envProduction); got != want {
		t.Errorf("QRCodeURL = %q, want %q", got, want)
	}
	if got := b.QRCodeURL(k, envHomologation); got != "https://portal.test?p="+senateKey+"|3|2" {
		t.Errorf("QRCodeURL homologation = %q", got)
	}
}

func TestDefaultQRCodeURL(t *testing.T) {
	k, _ := ParseAccessKey(senateKey)
	b := (SPURLBuilder{}).QRCodeURL(k, envProduction)
	if !strings.HasPrefix(b, DefaultSPQRBaseURL+"?p="+senateKey+"|3|") {
		t.Errorf("default URL = %q", b)
	}
}
