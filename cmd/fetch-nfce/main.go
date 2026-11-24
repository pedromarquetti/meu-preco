package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"meupreco/internal/logger"
	"meupreco/internal/nfce"
)

const (
	// cookieEnvVar lets an operator pass a diagnostic cookie without putting
	// its value in the process arguments (and therefore shell history).
	cookieEnvVar = "SEFAZ_DIAG_COOKIE"
)

// cliConfig is the fully validated command configuration. The cookie value and
// the full QR payload are kept for the request but must never be logged.
type cliConfig struct {
	baseURL      string
	userAgent    string
	cookie       string
	cookieSource string
	timeout      time.Duration
	minInterval  time.Duration
	logLevel     string
	logFormat    string
	retries      int
	key          nfce.AccessKey
	payload      string
	host         string
	path         string
	help         bool
}

func main() {
	cfg, err := parseArgs(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch-nfce: %v\n", err)
		os.Exit(1)
	}
	if cfg.help {
		return
	}

	log := newLogger(cfg.logLevel, cfg.logFormat)
	logStart(log, cfg)

	source, err := nfce.NewHTTPSource(nfce.HTTPSourceOptions{
		BaseURL:    cfg.baseURL,
		UserAgent:  cfg.userAgent,
		Cookie:     cfg.cookie,
		Limiter:    nfce.NewPortalLimiter(1, cfg.minInterval),
		MaxRetries: cfg.retries,
		Timeout:    cfg.timeout,
		Logger:     log,
	})
	if err != nil {
		log.Error("Failed to create Http", "Error", err)
		os.Exit(1)
	}

	body, err := source.Fetch(context.Background(), cfg.key, cfg.payload)
	if err != nil {
		log.Error("Source fetch error", "Error", err)
		os.Exit(1)
	}

	log.Info("Final Result: Success", "len", len(body))
}

// parseArgs parses the flags and positional arguments. getenv is injected so
// tests can exercise cookie resolution without touching the real environment.
func parseArgs(args []string, getenv func(string) string, output io.Writer) (cliConfig, error) {
	fs := flag.NewFlagSet("fetch-nfce", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: fetch-nfce [flags] <access_key> [payload]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	var (
		baseURL        = fs.String("u", nfce.DefaultSPQRBaseURL, "SEFAZ base URL")
		userAgent      = fs.String("a", nfce.DefaultUserAgent, "User agent for the request")
		cookieSource   = fs.String("c", "", "Cookie source: @path to a 0600 file (or set "+cookieEnvVar+")")
		timeoutStr     = fs.String("t", "30s", "Timeout per attempt (e.g., 15s, 60s)")
		minIntervalStr = fs.String("i", "10s", "Minimum spacing between attempts")
		logLevel       = fs.String("l", "info", "Set the logger's Log level")
		logFormat      = fs.String("f", "", "Set the logger's Log format (json or pretty print)")
		retries        = fs.Int("r", 1, "Maximum number of attempts/retries")
		help           = fs.Bool("h", false, "Print this help menu")
	)

	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if *help {
		fs.Usage()
		return cliConfig{help: true}, nil
	}

	key, payload, err := parseTarget(fs.Args())
	if err != nil {
		fs.Usage()
		return cliConfig{}, err
	}

	timeout, err := time.ParseDuration(*timeoutStr)
	if err != nil {
		return cliConfig{}, fmt.Errorf("invalid timeout: %w", err)
	}
	if timeout <= 0 {
		return cliConfig{}, fmt.Errorf("timeout must be positive")
	}

	minInterval, err := time.ParseDuration(*minIntervalStr)
	if err != nil {
		return cliConfig{}, fmt.Errorf("invalid minimum interval: %w", err)
	}
	if minInterval < 0 {
		return cliConfig{}, fmt.Errorf("minimum interval must be >= 0")
	}
	if *retries < 1 {
		return cliConfig{}, fmt.Errorf("retries must be >= 1")
	}

	cookie, source, err := resolveCookie(*cookieSource, getenv)
	if err != nil {
		return cliConfig{}, err
	}

	ua := strings.TrimSpace(*userAgent)
	if ua == "" {
		ua = nfce.DefaultUserAgent
	}

	host, path := baseHostPath(*baseURL)

	return cliConfig{
		baseURL:      *baseURL,
		userAgent:    ua,
		cookie:       cookie,
		cookieSource: source,
		timeout:      timeout,
		minInterval:  minInterval,
		logLevel:     *logLevel,
		logFormat:    *logFormat,
		retries:      *retries,
		key:          key,
		payload:      payload,
		host:         host,
		path:         path,
	}, nil
}

// parseTarget accepts "<access_key> [payload]" (and, for convenience, a lone
// key or QR payload). When both arguments are present the payload's embedded
// key must match the first argument; a mismatch is rejected rather than
// silently fetching a different receipt.
func parseTarget(args []string) (nfce.AccessKey, string, error) {
	switch len(args) {
	case 0:
		return nfce.AccessKey{}, "", fmt.Errorf("missing <access_key> or QR payload")
	case 1:
		raw := strings.TrimSpace(args[0])
		if key, err := nfce.ParseAccessKey(raw); err == nil {
			return key, "", nil
		}
		payload, key, err := nfce.ParseQRPayload(raw)
		if err != nil {
			return nfce.AccessKey{}, "", fmt.Errorf("invalid access key or QR payload: %w", err)
		}
		return key, payload, nil
	case 2:
		key, err := nfce.ParseAccessKey(strings.TrimSpace(args[0]))
		if err != nil {
			return nfce.AccessKey{}, "", fmt.Errorf("invalid access key: %w", err)
		}
		payload, payloadKey, err := nfce.ParseQRPayload(strings.TrimSpace(args[1]))
		if err != nil {
			return nfce.AccessKey{}, "", fmt.Errorf("invalid QR payload: %w", err)
		}
		if payloadKey.String() != key.String() {
			return nfce.AccessKey{}, "", fmt.Errorf("payload access key does not match the argument access key")
		}
		return key, payload, nil
	default:
		return nfce.AccessKey{}, "", fmt.Errorf("expected <access_key> [payload], got %d arguments", len(args))
	}
}

// resolveCookie reads a diagnostic cookie from the environment or from a
// private file. A literal -c value is rejected because it would land in shell
// history and the process argument list.
func resolveCookie(flagValue string, getenv func(string) string) (value, source string, err error) {
	if flagValue != "" {
		if !strings.HasPrefix(flagValue, "@") {
			return "", "", fmt.Errorf("-c accepts only @path to a cookie file; set %s to pass a value without exposing it in argv", cookieEnvVar)
		}

		path := strings.TrimPrefix(flagValue, "@")
		if path == "" {
			return "", "", fmt.Errorf("-c @path is missing the file path")
		}

		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", "", fmt.Errorf("read cookie file: %w", statErr)
		}
		if !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("cookie path %s is not a regular file", path)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return "", "", fmt.Errorf("cookie file %s must not be accessible by group/other (chmod 600), got %04o", path, perm)
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", "", fmt.Errorf("read cookie file: %w", readErr)
		}
		cookie := strings.TrimSpace(string(raw))
		if cookie == "" {
			return "", "", fmt.Errorf("cookie file %s is empty", path)
		}
		return cookie, "file", nil
	}

	if getenv != nil {
		if cookie := strings.TrimSpace(getenv(cookieEnvVar)); cookie != "" {
			return cookie, "env", nil
		}
	}

	return "", "none", nil
}

// baseHostPath extracts the host and path that will be logged. Query strings
// (which may carry the QR payload) are intentionally discarded.
func baseHostPath(raw string) (host, path string) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "/"
	}

	host = u.Host
	path = u.EscapedPath()
	if path == "" {
		path = "/"
	}

	return host, path
}

// logStart logs only non-secret request shape: access key, host/path, effective
// UA, cookie source and pacing. The cookie value and the full QR payload are
// never included.
func logStart(log *slog.Logger, cfg cliConfig) {
	log.Info("Starting diagnostic fetch.",
		"access_key", cfg.key.String(),
		"log_level", cfg.logLevel,
		"host", cfg.host,
		"path", cfg.path,
		"user_agent", cfg.userAgent,
		"cookie_source", cfg.cookieSource,
		"min_interval", cfg.minInterval,
		"max_retries", cfg.retries,
		"timeout", cfg.timeout,
	)
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level

	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	if format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(logger.NewPrettyHandler(os.Stdout, opts))
}
