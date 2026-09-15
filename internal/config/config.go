// Package config loads and validates server configuration from the
// environment. Every value has a sane default unless explicitly required.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the validated runtime configuration of the server.
type Config struct {
	Port        string
	LogLevel    string
	CORSOrigins []string

	// DatabaseURL and JWTSecret are consumed by later phases (DB in Phase 1,
	// auth in Phase 2). They are read now so the contract is stable and will
	// become required once those subsystems land (see requiredEnv below).
	DatabaseURL   string
	JWTSecret     string
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration

	// SEFAZFetchRPS caps how many requests per second the SP NFC-e portal
	// fetcher may issue (politeness/anti-ban knob).
	SEFAZFetchRPS int
}

// requiredEnv lists environment variables without which the process must not
// boot.
var requiredEnv = map[string]bool{}

// Load reads the process environment, applies defaults and validates.
// On any problem it returns an actionable error naming the offending
// variable; main() must abort on it.
func Load() (Config, error) {
	return load(envSnapshot())
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

	cfg := Config{
		Port:          envString(env, "PORT", "8080"),
		LogLevel:      envString(env, "LOG_LEVEL", "info"),
		CORSOrigins:   csvSlice(envString(env, "CORS_ORIGINS", "*")),
		DatabaseURL:   env["DATABASE_URL"],
		JWTSecret:     env["JWT_SECRET"],
		JWTAccessTTL:  jwtAccessTTL,
		JWTRefreshTTL: jwtRefreshTTL,
		SEFAZFetchRPS: sefaRPS,
	}
	return cfg, cfg.Validate()
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

	if c.JWTAccessTTL <= 0 {
		return fmt.Errorf("config: JWT_ACCESS_TTL must be a positive duration")
	}
	if c.JWTRefreshTTL <= 0 {
		return fmt.Errorf("config: JWT_REFRESH_TTL must be a positive duration")
	}
	if c.SEFAZFetchRPS < 1 {
		return fmt.Errorf("config: SEFAZ_FETCH_RPS must be >= 1")
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
