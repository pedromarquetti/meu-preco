package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(map[string]string{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if len(cfg.CORSOrigins) != 1 || cfg.CORSOrigins[0] != "*" {
		t.Errorf("CORSOrigins = %v, want [*]", cfg.CORSOrigins)
	}
	if cfg.JWTAccessTTL != 15*time.Minute {
		t.Errorf("JWTAccessTTL = %v, want 15m", cfg.JWTAccessTTL)
	}
	if cfg.JWTRefreshTTL != 30*24*time.Hour {
		t.Errorf("JWTRefreshTTL = %v, want 720h", cfg.JWTRefreshTTL)
	}
	if cfg.SEFAZFetchRPS != 1 {
		t.Errorf("SEFAZFetchRPS = %d, want 1", cfg.SEFAZFetchRPS)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(map[string]string{
		"PORT":            "9090",
		"LOG_LEVEL":       "debug",
		"CORS_ORIGINS":    "https://a.example.com, https://b.example.com",
		"JWT_ACCESS_TTL":  "5m",
		"JWT_REFRESH_TTL": "1h",
		"SEFAZ_FETCH_RPS": "2",
		"DATABASE_URL":    "postgres://db",
		"JWT_SECRET":      "s3cret",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Port != "9090" || cfg.LogLevel != "debug" || cfg.SEFAZFetchRPS != 2 {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if len(cfg.CORSOrigins) != 2 || cfg.CORSOrigins[1] != "https://b.example.com" {
		t.Errorf("CORSOrigins = %v", cfg.CORSOrigins)
	}
	if cfg.JWTAccessTTL != 5*time.Minute || cfg.JWTRefreshTTL != time.Hour {
		t.Errorf("TTLs = %v / %v", cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	}
	if cfg.DatabaseURL != "postgres://db" || cfg.JWTSecret != "s3cret" {
		t.Errorf("DatabaseURL/JWTSecret not read: %+v", cfg)
	}
}

func TestLoadBadPort(t *testing.T) {
	_, err := load(map[string]string{"PORT": "abc"})
	if err == nil || !strings.Contains(err.Error(), "PORT") {
		t.Fatalf("expected PORT error, got %v", err)
	}
	_, err = load(map[string]string{"PORT": "0"})
	if err == nil || !strings.Contains(err.Error(), "PORT") {
		t.Fatalf("expected PORT error for 0, got %v", err)
	}
}

func TestLoadBadLogLevel(t *testing.T) {
	_, err := load(map[string]string{"LOG_LEVEL": "loud"})
	if err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("expected LOG_LEVEL error, got %v", err)
	}
}

func TestLoadBadDuration(t *testing.T) {
	_, err := load(map[string]string{"JWT_ACCESS_TTL": "soon"})
	if err == nil || !strings.Contains(err.Error(), "JWT_ACCESS_TTL") {
		t.Fatalf("expected JWT_ACCESS_TTL error, got %v", err)
	}
}

func TestLoadBadRPS(t *testing.T) {
	_, err := load(map[string]string{"SEFAZ_FETCH_RPS": "0"})
	if err == nil || !strings.Contains(err.Error(), "SEFAZ_FETCH_RPS") {
		t.Fatalf("expected SEFAZ_FETCH_RPS error, got %v", err)
	}
}

func TestLoadRequiredEnv(t *testing.T) {
	key := "MEUPRECO_TEST_REQUIRED"

	requiredEnv[key] = true
	defer delete(requiredEnv, key)

	_, err := load(map[string]string{})
	if err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("expected error naming missing %s, got %v", key, err)
	}

	cfg, err := load(map[string]string{key: "present"})
	if err != nil {
		t.Fatalf("expected success when set, got %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q", cfg.Port)
	}
}

func TestCSVSlice(t *testing.T) {
	got := csvSlice(" , a, b,, ")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("csvSlice = %v, want [a b]", got)
	}
}
