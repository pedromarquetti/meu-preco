package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meupreco/internal/nfce"
)

const (
	testKey  = "35241152323688000144650020000002571115675901"
	otherKey = "31240112345000123456650010000002571123456785"
)

func noEnv(string) string { return "" }

func TestParseTarget(t *testing.T) {
	payload := testKey + "|3|1"

	t.Run("key only", func(t *testing.T) {
		key, gotPayload, err := parseTarget([]string{testKey})
		if err != nil {
			t.Fatalf("parseTarget: %v", err)
		}
		if key.String() != testKey {
			t.Errorf("key = %q, want %q", key.String(), testKey)
		}
		if gotPayload != "" {
			t.Errorf("payload = %q, want empty for a key-only argument", gotPayload)
		}
	})

	t.Run("masked key only", func(t *testing.T) {
		masked := "3524 1152 3236 8800 0144 6500 2000 0002 5711 1567 5901"
		key, gotPayload, err := parseTarget([]string{masked})
		if err != nil {
			t.Fatalf("parseTarget: %v", err)
		}
		if key.String() != testKey || gotPayload != "" {
			t.Errorf("key/payload = %q/%q", key.String(), gotPayload)
		}
	})

	t.Run("payload only", func(t *testing.T) {
		key, gotPayload, err := parseTarget([]string{payload})
		if err != nil {
			t.Fatalf("parseTarget: %v", err)
		}
		if key.String() != testKey || gotPayload != payload {
			t.Errorf("key/payload = %q/%q, want %q/%q", key.String(), gotPayload, testKey, payload)
		}
	})

	t.Run("full URL payload only", func(t *testing.T) {
		fullURL := nfce.DefaultSPQRBaseURL + "?p=" + testKey + "%7C3%7C1"
		key, gotPayload, err := parseTarget([]string{fullURL})
		if err != nil {
			t.Fatalf("parseTarget: %v", err)
		}
		if key.String() != testKey || gotPayload != payload {
			t.Errorf("key/payload = %q/%q, want %q/%q", key.String(), gotPayload, testKey, payload)
		}
	})

	t.Run("key and matching payload", func(t *testing.T) {
		key, gotPayload, err := parseTarget([]string{testKey, payload})
		if err != nil {
			t.Fatalf("parseTarget: %v", err)
		}
		if key.String() != testKey || gotPayload != payload {
			t.Errorf("key/payload = %q/%q", key.String(), gotPayload)
		}
	})

	t.Run("conflicting payload", func(t *testing.T) {
		conflicting := otherKey + "|3|1"
		_, _, err := parseTarget([]string{testKey, conflicting})
		if err == nil {
			t.Fatal("expected a conflict error")
		}
		if strings.Contains(err.Error(), conflicting) || strings.Contains(err.Error(), otherKey) {
			t.Errorf("error leaks the payload/key: %v", err)
		}
	})

	t.Run("missing argument", func(t *testing.T) {
		if _, _, err := parseTarget(nil); err == nil {
			t.Fatal("expected a missing-argument error")
		}
	})

	t.Run("too many arguments", func(t *testing.T) {
		if _, _, err := parseTarget([]string{testKey, payload, "extra"}); err == nil {
			t.Fatal("expected an argument-count error")
		}
	})
}

func TestParseArgsDefaults(t *testing.T) {
	cfg, err := parseArgs([]string{testKey}, noEnv, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if cfg.key.String() != testKey || cfg.payload != "" {
		t.Errorf("key/payload = %q/%q", cfg.key.String(), cfg.payload)
	}
	if cfg.userAgent != nfce.DefaultUserAgent {
		t.Errorf("userAgent = %q, want %q", cfg.userAgent, nfce.DefaultUserAgent)
	}
	if cfg.minInterval != 10*time.Second {
		t.Errorf("minInterval = %v, want 10s", cfg.minInterval)
	}
	if cfg.timeout != 30*time.Second {
		t.Errorf("timeout = %v, want 30s", cfg.timeout)
	}
	if cfg.retries != 1 {
		t.Errorf("retries = %d, want 1", cfg.retries)
	}
	if cfg.cookieSource != "none" {
		t.Errorf("cookieSource = %q, want none", cfg.cookieSource)
	}
	if cfg.host == "" || cfg.path == "" {
		t.Errorf("host/path = %q/%q", cfg.host, cfg.path)
	}
}

func TestParseArgsOverrides(t *testing.T) {
	cfg, err := parseArgs([]string{
		"-a", "  MeuPreco/diagnostic  ",
		"-i", "30s",
		"-r", "2",
		testKey,
	}, noEnv, io.Discard)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if cfg.userAgent != "MeuPreco/diagnostic" {
		t.Errorf("userAgent = %q", cfg.userAgent)
	}
	if cfg.minInterval != 30*time.Second || cfg.retries != 2 {
		t.Errorf("interval/retries = %v/%d", cfg.minInterval, cfg.retries)
	}
}

func TestResolveCookie(t *testing.T) {
	t.Run("literal flag value rejected", func(t *testing.T) {
		const cookie = "ASP.NET_SessionId=super-secret"
		_, _, err := resolveCookie(cookie, noEnv)
		if err == nil {
			t.Fatal("expected a literal-cookie error")
		}
		if strings.Contains(err.Error(), cookie) {
			t.Errorf("error leaks the cookie: %v", err)
		}
	})

	t.Run("env value", func(t *testing.T) {
		value, source, err := resolveCookie("", func(key string) string {
			if key == cookieEnvVar {
				return "  ASP.NET_SessionId=secret  "
			}
			return ""
		})
		if err != nil {
			t.Fatalf("resolveCookie: %v", err)
		}
		if value != "ASP.NET_SessionId=secret" || source != "env" {
			t.Errorf("value/source = %q/%q", value, source)
		}
	})

	t.Run("private file", func(t *testing.T) {
		path := writeCookieFile(t, "ASP.NET_SessionId=secret\n", 0o600)
		value, source, err := resolveCookie("@"+path, noEnv)
		if err != nil {
			t.Fatalf("resolveCookie: %v", err)
		}
		if value != "ASP.NET_SessionId=secret" || source != "file" {
			t.Errorf("value/source = %q/%q", value, source)
		}
	})

	t.Run("insecure file rejected", func(t *testing.T) {
		const cookie = "ASP.NET_SessionId=secret"
		path := writeCookieFile(t, cookie, 0o644)
		_, _, err := resolveCookie("@"+path, noEnv)
		if err == nil {
			t.Fatal("expected a permission error")
		}
		if strings.Contains(err.Error(), cookie) {
			t.Errorf("error leaks the cookie: %v", err)
		}
	})

	t.Run("missing @path", func(t *testing.T) {
		if _, _, err := resolveCookie("@", noEnv); err == nil {
			t.Fatal("expected an empty-path error")
		}
	})

	t.Run("none", func(t *testing.T) {
		value, source, err := resolveCookie("", noEnv)
		if err != nil || value != "" || source != "none" {
			t.Errorf("value/source/err = %q/%q/%v", value, source, err)
		}
	})
}

func TestParseArgsRejectsLiteralCookie(t *testing.T) {
	const cookie = "ASP.NET_SessionId=super-secret"
	var output bytes.Buffer

	_, err := parseArgs([]string{"-c", cookie, testKey}, noEnv, &output)
	if err == nil {
		t.Fatal("expected a literal-cookie error")
	}
	if strings.Contains(err.Error(), cookie) || strings.Contains(output.String(), cookie) {
		t.Errorf("cookie leaked in error or usage output: err=%v output=%s", err, output.String())
	}
}

func TestLogStartOmitsSecrets(t *testing.T) {
	key, err := nfce.ParseAccessKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	const cookie = "ASP.NET_SessionId=super-secret"
	payload := testKey + "|3|1|deadbeef"

	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, nil))
	logStart(log, cliConfig{
		key:          key,
		payload:      payload,
		cookie:       cookie,
		cookieSource: "env",
		logLevel:     "debug",
		host:         "portal.example",
		path:         "/qrcode",
		userAgent:    nfce.DefaultUserAgent,
		minInterval:  10 * time.Second,
		retries:      1,
		timeout:      30 * time.Second,
	})

	got := output.String()
	if !strings.Contains(got, testKey) {
		t.Errorf("log should include the access key: %s", got)
	}
	for _, secret := range []string{cookie, payload, "deadbeef"} {
		if strings.Contains(got, secret) {
			t.Errorf("log leaks %q: %s", secret, got)
		}
	}
}

func writeCookieFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cookie")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
