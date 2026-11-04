package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"meupreco/internal/logger"
	"meupreco/internal/nfce"
)

func init() {
	// Override the default usage message to include positional arguments.
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: fetch-nfce [flags] <access_key> [payload]\n\n")
		fmt.Fprintln(os.Stderr, "Flags:")
		flag.PrintDefaults()
	}
}

func main() {
	baseURL := flag.String("u", nfce.DefaultSPQRBaseURL, "SEFAZ base URL")
	userAgent := flag.String("a", "", "User agent for the request")
	timeoutStr := flag.String("t", "30s", "Timeout per attempt (e.g., 15s, 60s)")
	logLevel := flag.String("l", "info", "Set the logger's Log level")
	logFormat := flag.String("f", "", "Set the logger's Log format (json or pretty print)")
	retries := flag.Int("r", 3, "Maximum number of attempts/retries")
	help := flag.Bool("h", false, "Print this help menu")

	flag.Parse()

	args := flag.Args()

	if *help {
		flag.Usage()
		return
	}

	if len(args) < 1 {
		fmt.Println("Need to specify Acees Key or QR payload")
		flag.Usage()
		return
	}

	log := newLogger(*logLevel, *logFormat)

	// User-specified 44-digit key OR QR payload
	payload := args[0]

	// accept both raw 44 digit key OR qr
	key, errK := nfce.ParseAccessKey(payload)
	qr, _, errQ := nfce.ParseQRPayload(payload)

	if errK != nil && errQ != nil {
		fmt.Fprintf(os.Stderr, "Parse Error - %v\n", errK)
		os.Exit(1)
	}

	timeout, err := time.ParseDuration(*timeoutStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid timeout: %v\n", err)
		os.Exit(1)
	}

	opts := nfce.HTTPSourceOptions{
		BaseURL:    *baseURL,
		UserAgent:  *userAgent,
		MaxRetries: *retries,
		Timeout:    timeout,
		Logger:     log,
	}

	source, err := nfce.NewHTTPSource(opts)
	if err != nil {
		log.Error("Failed to create Http", "Error", err)
		os.Exit(1)
	}

	// Use a context that allows for retries and backoff time within the total timeout if needed,
	// but usually we want each attempt or sequence of attempts to respect some bounds.
	ctx := context.Background()

	log.Info("Starting diagnostic fetch.",
		"accessKey", payload,
		"log_level", *logLevel,
		"NfcePortal", *baseURL,
		"UA", *userAgent,
		"MaxRetries", *retries,
		"Timeout", timeout,
	)

	body, err := source.Fetch(ctx, key, qr)
	if err != nil {
		log.Error("Source fetch error",
			"Error", err,
		)
		os.Exit(1)
	}

	log.Info("Final Result: Success",
		"len", len(body),
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
