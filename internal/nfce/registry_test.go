package nfce

import "testing"

func TestRegistryCoversAllParticipants(t *testing.T) {
	rows := Registry()
	if len(rows) != 27 {
		t.Fatalf("registry has %d UFs, want 27 NFC-e participants", len(rows))
	}

	seen := map[string]bool{}
	for _, cfg := range rows {
		if seen[cfg.UF] {
			t.Errorf("duplicate UF %s", cfg.UF)
		}
		seen[cfg.UF] = true

		if cfg.UF == "" || cfg.UFName == "" || cfg.FullName == "" {
			t.Errorf("UF %s has empty name fields", cfg.UF)
		}
		if cfg.QRBaseURL == "" {
			t.Errorf("UF %s has no production QR base URL", cfg.UF)
		}
		if cfg.ParamShape != qrParamP && cfg.ParamShape != qrParamChNFe {
			t.Errorf("UF %s has invalid param shape", cfg.UF)
		}
		if cfg.Portal == portalNone {
			t.Errorf("UF %s has no portal classification", cfg.UF)
		}
	}
}

func TestRegistrySPOnlySupported(t *testing.T) {
	for _, cfg := range Registry() {
		if cfg.Supported && cfg.UF != "35" {
			t.Errorf("UF %s unexpectedly marked supported", cfg.UF)
		}
		if cfg.UF == "35" && !cfg.Supported {
			t.Errorf("UF 35 (SP) must be supported")
		}
	}

	sp, ok := LookupUF("35")
	if !ok {
		t.Fatal("LookupUF(35) not found")
	}
	if sp.Portal != portalHTMLFamily {
		t.Errorf("SP portal = %d, want the resumo HTML family", sp.Portal)
	}
	if sp.QRBaseURL != DefaultSPQRBaseURL {
		t.Errorf("SP base %q, want the default %q", sp.QRBaseURL, DefaultSPQRBaseURL)
	}
	if !SupportedUF("35") {
		t.Errorf("SupportedUF(35) = false")
	}
	if SupportedUF("31") || SupportedUF("43") || SupportedUF("33") {
		t.Errorf("an unsupported state reports supported")
	}
}

func TestRegistryLookupUnknown(t *testing.T) {
	if _, ok := LookupUF("99"); ok {
		t.Errorf("LookupUF(99) found a row")
	}
	if _, ok := LookupUF(""); ok {
		t.Errorf("LookupUF(\"\") found a row")
	}
}

func TestRegistryServerSideStatesDocumented(t *testing.T) {
	// PR and RS answer ?p= server-side but were never verified with a real
	// receipt; the registry must not claim they are supported.
	for _, uf := range []string{"41", "43"} {
		cfg, ok := LookupUF(uf)
		if !ok {
			t.Fatalf("UF %s missing", uf)
		}
		if cfg.Supported {
			t.Errorf("UF %s marked supported without fixture verification", uf)
		}
		if cfg.Portal != portalServerSide {
			t.Errorf("UF %s portal = %d, want portalServerSide", uf, cfg.Portal)
		}
	}
}
