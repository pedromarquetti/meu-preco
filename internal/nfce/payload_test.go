package nfce

import "testing"

func TestParseQRPayloadForms(t *testing.T) {
	v3 := senateKey + "|3|1"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"full URL (decoded)", "https://www.nfce.fazenda.sp.gov.br/qrcode?p=" + v3, v3},
		{"full URL (?p= not last)", "https://www.nfce.fazenda.sp.gov.br/qrcode?sem=1&p=" + v3, v3},
		{"full URL (URL-encoded)", "https://www.nfce.fazenda.sp.gov.br/qrcode?p=" + senateKey + "%7C3%7C1", v3},
		{"raw payload decoded", v3, v3},
		{"raw payload URL-encoded", senateKey + "%7C3%7C1", v3},
		{"bare 44-digit key", senateKey, v3},
		{"bare key with outer spaces", "  " + senateKey + "  ", v3},
		{"v2 payload preserved", senateKey + "|2|1|4|af1234", senateKey + "|2|1|4|af1234"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, key, err := ParseQRPayload(tc.in)
			if err != nil {
				t.Fatalf("ParseQRPayload(%q): %v", tc.in, err)
			}
			if payload != tc.want {
				t.Errorf("payload = %q, want %q", payload, tc.want)
			}
			if key.String() != senateKey {
				t.Errorf("key = %q, want %q", key, senateKey)
			}
		})
	}
}

func TestParseQRPayloadExtractsNonSPKey(t *testing.T) {
	const mgKey = "31240112345000123456650010000002571123456785"
	payload, key, err := ParseQRPayload(mgKey + "|3|1")
	if err != nil {
		t.Fatalf("ParseQRPayload: %v", err)
	}
	if payload != mgKey+"|3|1" || key.UFCode() != "31" {
		t.Errorf("got payload %q key %q", payload, key)
	}
}

func TestParseQRPayloadRejects(t *testing.T) {
	bad := []string{
		"",
		"   ",
		"banana",
		"42|3|1",
		senateKey[:43],
		senateKey[:43] + "2|3|1", // tampered DV
		senateKey + "|3|1|./x",
		senateKey + "|3|",
		senateKey + "|",
		"https://portal.test?p=",   // empty p value
		senateKey + "|3|1?p=09876", // stray p before end is treated as part of segments
	}
	for _, in := range bad {
		t.Run(in, func(t *testing.T) {
			if _, _, err := ParseQRPayload(in); err == nil {
				t.Errorf("ParseQRPayload(%q) accepted, want error", in)
			}
		})
	}
}
