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

	// DatabaseURL is built from POSTGRES_USERNAME, POSTGRES_PASSWORD and
	// DATABASE_NAME (plus optional POSTGRES_HOST/POSTGRES_PORT).
	DatabaseURL string

	// JWTSecret signs access+refresh tokens (Phase 3); >= 32 chars.
	JWTSecret     string
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration

	// AdminEmail/AdminPassword seed the first admin account at boot when the
	// users table is empty, so the admin is available before the server starts
	// serving. Both are required (see requiredEnv).
	AdminEmail    string
	AdminPassword string

	// SEFAZFetchRPS caps how many requests per second the SP NFC-e portal
	// fetcher may issue (politeness/anti-ban knob).
	SEFAZFetchRPS int

	// SefazBaseURL overrides the default SP NFC-e consultation QR URL. It is
	// optional (tests and homologation are the main use cases).
	SefazBaseURL string
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
		SEFAZFetchRPS: sefaRPS,
		SefazBaseURL:  env["SEFAZ_BASE_URL"],
	}
	return cfg, cfg.Validate()
}

// buildPostgresURL assembles postgres://user:pass@host:port/db?sslmode=disable
// from POSTGRES_USERNAME, POSTGRES_PASSWORD and DATABASE_NAME (required),
// POSTGRES_HOST (default localhost) and POSTGRES_PORT (default 5432). Missing
// variables or a malformed port produce an explicit, actionable error.
func buildPostgresURL(env map[string]string) (string, error) {
	var missing []string

	for _, k := range []string{"POSTGRES_USERNAME", "POSTGRES_PASSWORD", "DATABASE_NAME"} {
		if env[k] == "" {
			missing = append(missing, k)
		}
	}

	if len(missing) > 0 {
		return "", fmt.Errorf("config: missing required PostgreSQL environment variable(s): %s",
			strings.Join(missing, ", "))
	}

	host := envString(env, "POSTGRES_HOST", "localhost")
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

func csvSlice(v string) []string {
	var out []string
	for split := range strings.SplitSeq(v, ",") {
		if s := strings.TrimSpace(split); s != "" {
			out = append(out, s)
		}
	}
	return out
}
