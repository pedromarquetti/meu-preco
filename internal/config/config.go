// Package config loads and validates server configuration from the
// environment. Every value has a sane default unless explicitly required.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the validated runtime configuration of the server.
type Config struct {
	Port        string
	LogLevel    string
	LogFormat   string
	CORSOrigins []string

	// DatabaseURL is built from POSTGRES_USERNAME, POSTGRES_PASSWORD,
	// DATABASE_NAME and POSTGRES_HOST (plus optional POSTGRES_PORT).
	DatabaseURL string

	// JWTSecret signs access+refresh tokens; >= 32 chars.
	JWTSecret     string
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration

	// AdminName/AdminEmail/AdminPassword seed the first admin account at boot
	// when no admin exists, so the admin is available before the server starts
	// serving. Email and password are required (see requiredEnv); AdminName is
	// optional and defaults to the email's local part.
	AdminName     string
	AdminEmail    string
	AdminPassword string

	// SEFAZFetchRPS caps how many requests per second the SP NFC-e portal
	// fetcher may issue (politeness/anti-ban knob).
	SEFAZFetchRPS int
	// SEFAZMaxRetries is the number of fetch attempts per receipt (>= 1).
	SEFAZMaxRetries int
	// SEFAZTimeout bounds each portal request (retries included).
	SEFAZTimeout time.Duration
	// SEFAZBackoffBase is the starting exponential retry delay.
	SEFAZBackoffBase time.Duration
	// SEFAZBackoffCap caps each exponential retry delay.
	SEFAZBackoffCap time.Duration

	// SefazBaseURL overrides the default SP NFC-e consultation QR URL. It is
	// optional (tests and homologation are the main use cases).
	SefazBaseURL string

	// PreviewTTL is how long a pending receipt draft stays valid before the
	// background sweep purges it (the client re-'new's it as a fresh draft).
	PreviewTTL time.Duration
	// PreviewGCInterval is how often the background goroutine sweeps expired
	// drafts (in addition to the write-time sweep).
	PreviewGCInterval time.Duration

	// RateLimitIPRPS/RateLimitIPBurst shape the global per-IP token bucket
	// applied to the /api/v1 tree. Zero means "unlimited" per key.
	RateLimitIPRPS   float64
	RateLimitIPBurst int
	// RateLimitUserRPS/RateLimitUserBurst shape the per-user token bucket
	// applied to authenticated routes (keyed by the JWT subject).
	RateLimitUserRPS   float64
	RateLimitUserBurst int

	// EnableOFFEnrich opts in to best-effort Open Food Facts enrichment of
	// gtin_new lines at draft time (cached per barcode). Off by default:
	// ingestion stays offline-first and deterministic.
	EnableOFFEnrich bool
}

// requiredEnv lists environment variables without which the process must not
// boot.
var requiredEnv = map[string]bool{
	"JWT_SECRET":     true,
	"ADMIN_EMAIL":    true,
	"ADMIN_PASSWORD": true,
}

// Load reads the process environment, applies defaults and validates.
// On any problem it returns an actionable error naming the offending
// variable; main() must abort on it.
func Load() (Config, error) {
	return load(envSnapshot())
}

// PostgresURLFromEnv builds the PostgreSQL DSN from the process environment
// using the same rules as Load, so auxiliary tools (cmd/migrate) share one
// source of truth. It errors, explicitly, when a required variable is missing.
func PostgresURLFromEnv() (string, error) {
	return buildPostgresURL(envSnapshot())
}

func envSnapshot() map[string]string {
	env := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		if k, v, found := strings.Cut(kv, "="); found {
			env[k] = v
		}
	}

	return env
}

func load(env map[string]string) (Config, error) {
	for key := range requiredEnv {
		if env[key] == "" {
			return Config{}, fmt.Errorf("config: required environment variable %s is not set", key)
		}
	}

	jwtAccessTTL, err := envDuration(env, "JWT_ACCESS_TTL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}

	jwtRefreshTTL, err := envDuration(env, "JWT_REFRESH_TTL", 30*24*time.Hour)
	if err != nil {
		return Config{}, err
	}

	sefaRPS, err := envInt(env, "SEFAZ_FETCH_RPS", 1)
	if err != nil {
		return Config{}, err
	}

	sefaRetries, err := envInt(env, "SEFAZ_MAX_RETRIES", 3)
	if err != nil {
		return Config{}, err
	}

	sefaTimeout, err := envDuration(env, "SEFAZ_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}

	sefaBackoffBase, err := envDuration(env, "SEFAZ_BACKOFF_BASE", 200*time.Millisecond)
	if err != nil {
		return Config{}, err
	}

	sefaBackoffCap, err := envDuration(env, "SEFAZ_BACKOFF_CAP", 2*time.Second)
	if err != nil {
		return Config{}, err
	}

	previewTTL, err := envDuration(env, "PREVIEWS_TTL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}

	previewGC, err := envDuration(env, "PREVIEW_GC_INTERVAL", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}

	rlIPRPS, err := envFloat(env, "RATE_LIMIT_IP_RPS", 5)
	if err != nil {
		return Config{}, err
	}

	rlIPBurst, err := envInt(env, "RATE_LIMIT_IP_BURST", 300)
	if err != nil {
		return Config{}, err
	}

	rlUserRPS, err := envFloat(env, "RATE_LIMIT_USER_RPS", 20)
	if err != nil {
		return Config{}, err
	}

	rlUserBurst, err := envInt(env, "RATE_LIMIT_USER_BURST", 1000)
	if err != nil {
		return Config{}, err
	}

	enrichOFF, err := envBool(env, "ENRICH_OFF", false)
	if err != nil {
		return Config{}, err
	}

	dsn, err := buildPostgresURL(env)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Port:          envString(env, "PORT", "8080"),
		LogLevel:      envString(env, "LOG_LEVEL", "info"),
		LogFormat:     envString(env, "LOG_FORMAT", "pretty"),
		CORSOrigins:   csvSlice(envString(env, "CORS_ORIGINS", "*")),
		DatabaseURL:   dsn,
		JWTSecret:     env["JWT_SECRET"],
		JWTAccessTTL:  jwtAccessTTL,
		JWTRefreshTTL: jwtRefreshTTL,
		AdminEmail:    env["ADMIN_EMAIL"],
		AdminPassword: env["ADMIN_PASSWORD"],
		AdminName:     env["ADMIN_NAME"],
		SEFAZFetchRPS: sefaRPS,
		SefazBaseURL:  env["SEFAZ_BASE_URL"],

		SEFAZMaxRetries:  sefaRetries,
		SEFAZTimeout:     sefaTimeout,
		SEFAZBackoffBase: sefaBackoffBase,
		SEFAZBackoffCap:  sefaBackoffCap,

		PreviewTTL:        previewTTL,
		PreviewGCInterval: previewGC,

		RateLimitIPRPS:     rlIPRPS,
		RateLimitIPBurst:   rlIPBurst,
		RateLimitUserRPS:   rlUserRPS,
		RateLimitUserBurst: rlUserBurst,

		EnableOFFEnrich: enrichOFF,
	}
	return cfg, cfg.Validate()
}

// buildPostgresURL assembles postgres://user:pass@host:port/db?sslmode=disable
// from POSTGRES_USERNAME, POSTGRES_PASSWORD, DATABASE_NAME and POSTGRES_HOST
// (all required) plus POSTGRES_PORT (default 5432). Missing variables or a
// malformed port produce an explicit, actionable error.
//
// POSTGRES_HOST has no default on purpose: "localhost" silently points at
// the wrong machine as soon as the process runs in a container, so the host
// must always be stated explicitly.
func buildPostgresURL(env map[string]string) (string, error) {
	var missing []string

	for _, k := range []string{"POSTGRES_USERNAME", "POSTGRES_PASSWORD", "DATABASE_NAME", "POSTGRES_HOST"} {
		if env[k] == "" {
			missing = append(missing, k)
		}
	}

	if len(missing) > 0 {
		return "", fmt.Errorf("config: missing required PostgreSQL environment variable(s): %s",
			strings.Join(missing, ", "))
	}

	host := env["POSTGRES_HOST"]
	port := envString(env, "POSTGRES_PORT", "5432")

	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("config: POSTGRES_PORT must be a number, got %q", port)
	}

	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(env["POSTGRES_USERNAME"], env["POSTGRES_PASSWORD"]),
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + env["DATABASE_NAME"],
	}

	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// Validate checks the assembled Config for semantic errors.
func (c Config) Validate() error {
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("config: PORT must be a number between 1 and 65535, got %q", c.Port)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: LOG_LEVEL must be one of debug|info|warn|error, got %q", c.LogLevel)
	}

	switch c.LogFormat {
	case "pretty", "json":
	default:
		return fmt.Errorf("config: LOG_FORMAT must be one of pretty|json, got %q", c.LogFormat)
	}

	if c.JWTAccessTTL <= 0 {
		return fmt.Errorf("config: JWT_ACCESS_TTL must be a positive duration")
	}
	if c.JWTRefreshTTL <= 0 {
		return fmt.Errorf("config: JWT_REFRESH_TTL must be a positive duration")
	}
	if len(c.JWTSecret) < 32 {
		return fmt.Errorf("config: JWT_SECRET must be at least 32 characters")
	}
	if c.SEFAZFetchRPS < 1 {
		return fmt.Errorf("config: SEFAZ_FETCH_RPS must be >= 1")
	}
	if c.SEFAZMaxRetries < 1 {
		return fmt.Errorf("config: SEFAZ_MAX_RETRIES must be >= 1")
	}
	if c.SEFAZTimeout <= 0 {
		return fmt.Errorf("config: SEFAZ_TIMEOUT must be a positive duration")
	}
	if c.SEFAZBackoffBase < 0 {
		return fmt.Errorf("config: SEFAZ_BACKOFF_BASE must be >= 0")
	}
	if c.SEFAZBackoffCap < c.SEFAZBackoffBase {
		return fmt.Errorf("config: SEFAZ_BACKOFF_CAP must be >= SEFAZ_BACKOFF_BASE")
	}
	if c.PreviewTTL <= 0 {
		return fmt.Errorf("config: PREVIEWS_TTL must be a positive duration")
	}
	if c.PreviewGCInterval <= 0 {
		return fmt.Errorf("config: PREVIEW_GC_INTERVAL must be a positive duration")
	}
	if c.RateLimitIPRPS < 0 || c.RateLimitUserRPS < 0 {
		return fmt.Errorf("config: RATE_LIMIT_*_RPS must be >= 0")
	}
	if c.RateLimitIPBurst < 0 || c.RateLimitUserBurst < 0 {
		return fmt.Errorf("config: RATE_LIMIT_*_BURST must be >= 0")
	}
	if c.SefazBaseURL != "" {
		u, err := url.Parse(c.SefazBaseURL)

		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("config: SEFAZ_BASE_URL must be an http(s) URL, got %q", c.SefazBaseURL)
		}
	}

	if c.DatabaseURL != "" {
		u, err := url.Parse(c.DatabaseURL)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.Host == "" {
			return fmt.Errorf("config: DATABASE_URL must be a postgres:// URL, got %q", c.DatabaseURL)
		}
	}

	return nil
}

func envString(env map[string]string, key, default_val string) string {
	if v, ok := env[key]; ok && v != "" {
		return v
	}
	return default_val
}

func envDuration(env map[string]string, key string, default_duration time.Duration) (time.Duration, error) {
	v := env[key]
	if v == "" {
		return default_duration, nil
	}

	duration, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s %q: %w", key, v, err)
	}

	return duration, nil
}

func envInt(env map[string]string, key string, def int) (int, error) {
	v := env[key]
	if v == "" {
		return def, nil
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s %q: %w", key, v, err)
	}

	return n, nil
}

func envFloat(env map[string]string, key string, def float64) (float64, error) {
	v := env[key]
	if v == "" {
		return def, nil
	}

	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("config: invalid %s %q: %w", key, v, err)
	}

	return f, nil
}

func envBool(env map[string]string, key string, def bool) (bool, error) {
	v := env[key]
	if v == "" {
		return def, nil
	}

	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: invalid %s %q: %w", key, v, err)
	}

	return b, nil
}

func csvSlice(v string) []string {
	var out []string
	for split := range strings.SplitSeq(v, ",") {
		if s := strings.TrimSpace(split); s != "" {
			out = append(out, s)
		}
	}
	return out
}
