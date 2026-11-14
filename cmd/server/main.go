// Command server runs the meupreco API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"meupreco/internal/auth"
	"meupreco/internal/config"
	"meupreco/internal/domain"
	"meupreco/internal/httpapi"
	"meupreco/internal/service"
	pstore "meupreco/internal/store/postgres"

	"github.com/lmittmann/tint"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error while running meupreco: ", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()

	if err != nil {
		return err
	}

	log := newLogger(os.Stdout, cfg.LogLevel, cfg.LogFormat)

	// Open the database pool. It is lazy (no round-trip yet), so boot succeeds
	// even while PostgreSQL is down; readyz reports 503 until Ping succeeds.
	db, err := pstore.New(pstore.Options{DSN: cfg.DatabaseURL})

	if err != nil {
		return err
	}

	defer db.Close()

	// Seed the initial admin (from the required ADMIN_EMAIL/ADMIN_PASSWORD env)
	// before the listener starts, so the admin can log in on first boot. Once
	// any user exists it is a no-op and every subsequent boot skips it.
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := seedAdmin(seedCtx, log, db.Users(), cfg); err != nil {
		seedCancel()
		return err
	}
	seedCancel()

	// Background sweep of expired receipt drafts, on top of the write-time
	// sweep the service performs before each draft/commit. Cancelled when the
	// process shuts down.
	gcCtx, cancelGC := context.WithCancel(context.Background())
	defer cancelGC()

	go func() {
		if err := service.RunPreviewGC(gcCtx, log, db.Previews(), cfg.PreviewGCInterval); err != nil {
			log.Warn("preview GC stopped", "err", err)
		}
	}()

	// TODO: implement configurable root path for API
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.New(cfg, log, db),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)

	go func() {
		log.Info("server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Info("received signal, shutting down", "signal", sig.String())
	case err := <-errCh:
		return fmt.Errorf("server failed: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)

	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}

	log.Info("server stopped")
	return nil
}

// seedAdmin creates the initial admin account (role "admin") from the
// mandatory ADMIN_EMAIL/ADMIN_PASSWORD env the first time the database is
// empty, before the HTTP listener starts. It logs and is a no-op once any
// account exists, so it is safe on every boot and across replicas idempotently.
func seedAdmin(ctx context.Context, log *slog.Logger, users domain.UserStore, cfg config.Config) error {
	n, err := users.CountAdmin(ctx)
	if err != nil {
		return fmt.Errorf("seed admin: count admins: %w", err)
	}
	if n > 0 {
		log.Info("at least one admin already present, skipping seed", "users", n)
		return nil
	}

	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return fmt.Errorf("seed admin: hash password: %w", err)
	}

	email := strings.ToLower(strings.TrimSpace(cfg.AdminEmail))
	u := &domain.User{Name: adminSeedName(cfg.AdminName, email), Email: email, PasswordHash: hash, Role: "admin"}
	if err := users.Create(ctx, u); err != nil {
		return fmt.Errorf("seed admin: create: %w", err)
	}

	log.Info("seeded initial admin account", "email", email, "name", u.Name)
	return nil
}

// adminSeedName picks the initial admin's display name: ADMIN_NAME when set,
// otherwise the email's local part (users.name is NOT NULL).
func adminSeedName(configured, email string) string {
	if n := strings.TrimSpace(configured); n != "" {
		return n
	}
	if local, _, ok := strings.Cut(email, "@"); ok {
		if local = strings.TrimSpace(local); local != "" {
			return local
		}
	}
	return "admin"
}

// newLogger builds the process logger. LOG_FORMAT=json keeps the structured
// handler (for collectors); pretty uses tint, which keeps the human-readable
// key=value layout and colors it when w is a terminal. Redirected output stays
// plain so persisted logs contain no ANSI escapes.
func newLogger(w io.Writer, level, format string) *slog.Logger {
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

	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl}))
	}

	return slog.New(tint.NewTextHandler(w, &tint.Options{
		Level:      lvl,
		TimeFormat: "02-01-2006 15:04:05.000",
		NoColor:    !isTerminal(w),
	}))
}

// isTerminal reports whether w is an interactive terminal. Anything else
// (regular files, pipes, CI capture) is treated as persisted output.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
