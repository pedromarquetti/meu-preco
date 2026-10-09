package product

import "testing"

func TestNormalizeGTINValid(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"7896008038717", "07896008038717"}, // 13-digit GTIN-13 (bread)
		{"7896004021522", "07896004021522"}, // 13-digit GTIN-13
		{"7895551234560", "07895551234560"},
		{"7899876543215", "07899876543215"},
		{"07896008038717", "07896008038717"},   // already 14
		{" 789 6008038717 ", "07896008038717"}, // printed with spaces
	}
	for _, tc := range tests {
		got, ok := NormalizeGTIN(tc.in)
		if !ok {
			t.Errorf("NormalizeGTIN(%q) ok=false, want true", tc.in)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeGTIN(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeGTIN_7PrefixBulk(t *testing.T) {
	// CSG-region GTIN-13 covers: a payload ending in 9 → check "4" per GS1
	// rule (package "7890000000093" from the standard). Verifies the mod-10
	// weighting is applied with 3,1,3 starting from the rightmost payload.
	if got, ok := NormalizeGTIN("7890000000093"); !ok || got != "07890000000093" {
		t.Errorf("7890000000093 -> (%q,%v), want ok", got, ok)
	}
}

func TestNormalizeGTINBadCheckDigit(t *testing.T) {
	// Flip a digit of a valid GTIN: check digit no longer verifies.
	got, ok := NormalizeGTIN("7896008038713")
	if ok {
		t.Errorf("NormalizeGTIN(bad-check) = %q, want ok=false", got)
	}
}

func TestNormalizeGTINForeignPrefix(t *testing.T) {
	// Non-Brazilian prefixes (here a US UPC-A) must be rejected.
	if got, ok := NormalizeGTIN("036000291452"); ok {
		t.Errorf("NormalizeGTIN(US 036000291452) = %q, want ok=false", got)
	}
}

func TestNormalizeGTINGarbage(t *testing.T) {
	for _, in := range []string{
		"", "abc", "12345", "1234567890123456789", "0", "7891",
		"7899876543215X", "X7896008038717",
	} {
		if _, ok := NormalizeGTIN(in); ok {
			t.Errorf("NormalizeGTIN(%q) ok=true, want false", in)
		}
	}
}

func TestBarcodeDigits(t *testing.T) {
	if got := barcodeDigits("a78.9-0600 8038 717"); got != "" {
		t.Errorf("barcodeDigits(letters) = %q, want empty", got)
	}
	if got := barcodeDigits("789 6008 0387 17"); got != "7896008038717" {
		t.Errorf("barcodeDigits(separators) = %q", got)
	}
}
