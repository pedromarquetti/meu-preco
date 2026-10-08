// Command server runs the meupreco API server.
package main

import (
	"context"
	"errors"
	"fmt"
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
	"meupreco/internal/logger"
	pstore "meupreco/internal/store/postgres"
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

	log := newLogger(cfg.LogLevel, cfg.LogFormat)

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
	u := &domain.User{Email: email, PasswordHash: hash, Role: "admin"}
	if err := users.Create(ctx, u); err != nil {
		return fmt.Errorf("seed admin: create: %w", err)
	}

	log.Info("seeded initial admin account", "email", email)
	return nil
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
