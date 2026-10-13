package config

import (
	"maps"
	"strings"
	"testing"
	"time"
)

// env returns a valid base environment (POSTGRES_*, JWT_SECRET, ADMIN_EMAIL
// and ADMIN_PASSWORD are required by load) merged with test-specific
// overrides.
func env(extra map[string]string) map[string]string {
	m := map[string]string{
		"POSTGRES_USERNAME": "dev",
		"POSTGRES_PASSWORD": "dev",
		"DATABASE_NAME":     "meupreco",
		"JWT_SECRET":        "test-secret-with-at-least-32-characters",
		"ADMIN_EMAIL":       "admin@example.com",
		"ADMIN_PASSWORD":    "a-long-admin-password",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.LogFormat != "pretty" {
		t.Errorf("LogFormat = %q, want pretty", cfg.LogFormat)
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
	if cfg.SEFAZMaxRetries != 3 || cfg.SEFAZTimeout != 30*time.Second {
		t.Errorf("SEFAZ retry knobs = %d/%v, want 3/30s", cfg.SEFAZMaxRetries, cfg.SEFAZTimeout)
	}
	if cfg.SEFAZBackoffBase != 200*time.Millisecond || cfg.SEFAZBackoffCap != 2*time.Second {
		t.Errorf("SEFAZ backoff = %v/%v, want 200ms/2s", cfg.SEFAZBackoffBase, cfg.SEFAZBackoffCap)
	}
	if cfg.PreviewTTL != 15*time.Minute || cfg.PreviewGCInterval != 5*time.Minute {
		t.Errorf("preview lifecycle = %v/%v, want 15m/5m", cfg.PreviewTTL, cfg.PreviewGCInterval)
	}
	if cfg.RateLimitIPRPS != 5 || cfg.RateLimitIPBurst != 300 ||
		cfg.RateLimitUserRPS != 20 || cfg.RateLimitUserBurst != 1000 {
		t.Errorf("rate limits = %v/%v %v/%v, want 5/300 20/1000",
			cfg.RateLimitIPRPS, cfg.RateLimitIPBurst, cfg.RateLimitUserRPS, cfg.RateLimitUserBurst)
	}
	if cfg.EnableOFFEnrich {
		t.Errorf("EnableOFFEnrich = true, want false default")
	}
	if cfg.SefazBaseURL != "" {
		t.Errorf("SefazBaseURL = %q, want empty default", cfg.SefazBaseURL)
	}
	if cfg.DatabaseURL != "postgres://dev:dev@localhost:5432/meupreco?sslmode=disable" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}

func TestLoadSefazBaseURL(t *testing.T) {
	cfg, err := load(env(map[string]string{"SEFAZ_BASE_URL": "https://portal.example.gov.br/qrcode"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SefazBaseURL != "https://portal.example.gov.br/qrcode" {
		t.Errorf("SefazBaseURL = %q", cfg.SefazBaseURL)
	}
}

func TestLoadBadSefazBaseURL(t *testing.T) {
	for _, bad := range []string{"not a url", "ftp://x", "http://", "//missing-scheme"} {
		_, err := load(env(map[string]string{"SEFAZ_BASE_URL": bad}))
		if err == nil || !strings.Contains(err.Error(), "SEFAZ_BASE_URL") {
			t.Errorf("SEFAZ_BASE_URL=%q: expected error, got %v", bad, err)
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"PORT":            "9090",
		"LOG_LEVEL":       "debug",
		"CORS_ORIGINS":    "https://a.example.com, https://b.example.com",
		"JWT_ACCESS_TTL":  "5m",
		"JWT_REFRESH_TTL": "1h",
		"SEFAZ_FETCH_RPS": "2",
		"POSTGRES_HOST":   "db.internal",
		"POSTGRES_PORT":   "5433",
		"JWT_SECRET":      "an-overridden-secret-with-more-than-32-chars",
	}))
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
	if cfg.DatabaseURL != "postgres://dev:dev@db.internal:5433/meupreco?sslmode=disable" || cfg.JWTSecret != "an-overridden-secret-with-more-than-32-chars" {
		t.Errorf("DatabaseURL/JWTSecret not built/read: %+v", cfg)
	}
}

func TestLoadMissingPostgresEnv(t *testing.T) {
	// JWT_SECRET, ADMIN_EMAIL and ADMIN_PASSWORD are required too, but this
	// test isolates the PostgreSQL vars: supply them so buildPostgresURL is
	// what reports the missing DATABASE_*.
	_, err := load(map[string]string{
		"JWT_SECRET":     "a-valid-secret-with-at-least-32-chars",
		"ADMIN_EMAIL":    "admin@example.com",
		"ADMIN_PASSWORD": "a-long-admin-password",
	})
	if err == nil || !strings.Contains(err.Error(), "POSTGRES_USERNAME") ||
		!strings.Contains(err.Error(), "POSTGRES_PASSWORD") ||
		!strings.Contains(err.Error(), "DATABASE_NAME") {
		t.Fatalf("expected error naming all missing vars, got %v", err)
	}

	_, err = load(map[string]string{
		"JWT_SECRET":        "a-valid-secret-with-at-least-32-chars",
		"ADMIN_EMAIL":       "admin@example.com",
		"ADMIN_PASSWORD":    "a-long-admin-password",
		"POSTGRES_USERNAME": "dev",
		"POSTGRES_PASSWORD": "dev",
	})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_NAME") {
		t.Fatalf("expected error naming missing DATABASE_NAME, got %v", err)
	}
}

func TestLoadMissingAdminEnv(t *testing.T) {
	base := env(nil)

	for _, missing := range []string{"ADMIN_EMAIL", "ADMIN_PASSWORD"} {
		envMap := maps.Clone(base)
		delete(envMap, missing)
		_, err := load(envMap)
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("%s unset: expected required-env error, got %v", missing, err)
		}

		envMap = maps.Clone(base)
		envMap[missing] = ""
		_, err = load(envMap)
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("%s empty: expected required-env error, got %v", missing, err)
		}
	}
}

func TestLoadEscapesPostgresCredentials(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"POSTGRES_USERNAME": "dev",
		"POSTGRES_PASSWORD": "p@ss:with/special",
		"DATABASE_NAME":     "meupreco",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DatabaseURL != "postgres://dev:p%40ss%3Awith%2Fspecial@localhost:5432/meupreco?sslmode=disable" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}

func TestLoadBadPostgresPort(t *testing.T) {
	_, err := load(env(map[string]string{"POSTGRES_PORT": "543b"}))
	if err == nil || !strings.Contains(err.Error(), "POSTGRES_PORT") {
		t.Fatalf("expected POSTGRES_PORT error, got %v", err)
	}
}

func TestPostgresURLFromEnvMustNotPanic(t *testing.T) {
	t.Setenv("POSTGRES_USERNAME", "dev")
	t.Setenv("POSTGRES_PASSWORD", "dev")
	t.Setenv("DATABASE_NAME", "meupreco")
	dsn, err := PostgresURLFromEnv()
	if err != nil || dsn == "" {
		t.Fatalf("PostgresURLFromEnv = %q, %v", dsn, err)
	}
}

func TestLoadBadPort(t *testing.T) {
	_, err := load(env(map[string]string{"PORT": "abc"}))
	if err == nil || !strings.Contains(err.Error(), "PORT") {
		t.Fatalf("expected PORT error, got %v", err)
	}
	_, err = load(env(map[string]string{"PORT": "0"}))
	if err == nil || !strings.Contains(err.Error(), "PORT") {
		t.Fatalf("expected PORT error for 0, got %v", err)
	}
}

func TestLoadBadLogLevel(t *testing.T) {
	_, err := load(env(map[string]string{"LOG_LEVEL": "loud"}))
	if err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("expected LOG_LEVEL error, got %v", err)
	}
}

func TestLoadBadLogFormat(t *testing.T) {
	_, err := load(env(map[string]string{"LOG_FORMAT": "xml"}))
	if err == nil || !strings.Contains(err.Error(), "LOG_FORMAT") {
		t.Fatalf("expected LOG_FORMAT error, got %v", err)
	}
}

func TestLoadLogFormatOverride(t *testing.T) {
	cfg, err := load(env(map[string]string{"LOG_FORMAT": "json"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want json", cfg.LogFormat)
	}
}

func TestLoadBadDuration(t *testing.T) {
	_, err := load(env(map[string]string{"JWT_ACCESS_TTL": "soon"}))
	if err == nil || !strings.Contains(err.Error(), "JWT_ACCESS_TTL") {
		t.Fatalf("expected JWT_ACCESS_TTL error, got %v", err)
	}
}

func TestLoadBadRPS(t *testing.T) {
	_, err := load(env(map[string]string{"SEFAZ_FETCH_RPS": "0"}))
	if err == nil || !strings.Contains(err.Error(), "SEFAZ_FETCH_RPS") {
		t.Fatalf("expected SEFAZ_FETCH_RPS error, got %v", err)
	}
}

func TestLoadSefazTuning(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"SEFAZ_MAX_RETRIES":  "5",
		"SEFAZ_TIMEOUT":      "10s",
		"SEFAZ_BACKOFF_BASE": "50ms",
		"SEFAZ_BACKOFF_CAP":  "1s",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SEFAZMaxRetries != 5 || cfg.SEFAZTimeout != 10*time.Second ||
		cfg.SEFAZBackoffBase != 50*time.Millisecond || cfg.SEFAZBackoffCap != time.Second {
		t.Errorf("tuning = %d/%v/%v/%v", cfg.SEFAZMaxRetries, cfg.SEFAZTimeout,
			cfg.SEFAZBackoffBase, cfg.SEFAZBackoffCap)
	}
}

func TestLoadBadSefazTuning(t *testing.T) {
	cases := []struct {
		env   string
		value string
	}{
		{"SEFAZ_MAX_RETRIES", "0"}, {"SEFAZ_TIMEOUT", "0s"},
		{"SEFAZ_BACKOFF_CAP", "100ms"}, {"SEFAZ_BACKOFF_BASE", "-1ms"},
		{"SEFAZ_BACKOFF_BASE", "soon"},
	}
	for _, c := range cases {
		_, err := load(env(map[string]string{c.env: c.value}))
		if err == nil {
			t.Errorf("%s=%q: expected error", c.env, c.value)
		}
	}
}

func TestLoadPreviewLifecycle(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"PREVIEWS_TTL":        "30m",
		"PREVIEW_GC_INTERVAL": "90s",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PreviewTTL != 30*time.Minute || cfg.PreviewGCInterval != 90*time.Second {
		t.Errorf("preview = %v/%v", cfg.PreviewTTL, cfg.PreviewGCInterval)
	}
	_, err = load(env(map[string]string{"PREVIEWS_TTL": "0s"}))
	if err == nil {
		t.Error("PREVIEWS_TTL=0s accepted")
	}
}

func TestLoadRateLimits(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"RATE_LIMIT_IP_RPS":     "2.5",
		"RATE_LIMIT_IP_BURST":   "50",
		"RATE_LIMIT_USER_RPS":   "10",
		"RATE_LIMIT_USER_BURST": "200",
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.RateLimitIPRPS != 2.5 || cfg.RateLimitIPBurst != 50 ||
		cfg.RateLimitUserRPS != 10 || cfg.RateLimitUserBurst != 200 {
		t.Errorf("limits = %v/%v %v/%v", cfg.RateLimitIPRPS, cfg.RateLimitIPBurst,
			cfg.RateLimitUserRPS, cfg.RateLimitUserBurst)
	}
	_, err = load(env(map[string]string{"RATE_LIMIT_IP_RPS": "abc"}))
	if err == nil || !strings.Contains(err.Error(), "RATE_LIMIT_IP_RPS") {
		t.Error("bad RATE_LIMIT_IP_RPS accepted")
	}
}

func TestLoadEnableOFFEnrich(t *testing.T) {
	cfg, err := load(env(map[string]string{"ENRICH_OFF": "true"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.EnableOFFEnrich {
		t.Error("EnableOFFEnrich = false, want true")
	}
	_, err = load(env(map[string]string{"ENRICH_OFF": "maybe"}))
	if err == nil || !strings.Contains(err.Error(), "ENRICH_OFF") {
		t.Error("bad ENRICH_OFF accepted")
	}
}

func TestLoadBadJWTSecret(t *testing.T) {
	_, err := load(env(map[string]string{"JWT_SECRET": ""}))
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("expected JWT_SECRET missing error, got %v", err)
	}
	_, err = load(env(map[string]string{"JWT_SECRET": "too-short"}))
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("expected JWT_SECRET length error, got %v", err)
	}
}

func TestLoadAdminEnv(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"ADMIN_EMAIL":    "ops@example.com",
		"ADMIN_PASSWORD": "s3cret-admin",
	}))
	if err != nil {
		t.Fatalf("admin env rejected: %v", err)
	}
	if cfg.AdminEmail != "ops@example.com" || cfg.AdminPassword != "s3cret-admin" {
		t.Errorf("admin env not read: %+v", cfg)
	}
}

func TestLoadRequiredEnv(t *testing.T) {
	key := "MEUPRECO_TEST_REQUIRED"

	requiredEnv[key] = true
	defer delete(requiredEnv, key)

	_, err := load(env(nil))
	if err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("expected error naming missing %s, got %v", key, err)
	}

	cfg, err := load(env(map[string]string{key: "present"}))
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
