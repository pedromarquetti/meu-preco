// Package testdb provides shared helpers for PostgreSQL-backed tests. It is
// only imported by tests.
package testdb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"meupreco/migrations"
)

const dsnEnv = "MEUPRECO_TEST_DATABASE_URL"

// AdvisoryLockKey serializes schema resets across test packages sharing one
// test database ("meupreco" as a u64).
const AdvisoryLockKey int64 = 0x6d6575707265636f

// RequireDSN returns the configured test database URL, failing when it is
// missing or does not point at a disposable test database.
func RequireDSN() (string, error) {
	dsn := strings.TrimSpace(os.Getenv(dsnEnv))
	if dsn == "" {
		return "", fmt.Errorf("%s is required for postgres integration tests; point it at a disposable *_test database (see README)", dsnEnv)
	}
	if err := ValidateTestDSN(dsn); err != nil {
		return "", err
	}
	return dsn, nil
}

// ValidateTestDSN refuses destructive operations against non-test databases.
func ValidateTestDSN(dsn string) error {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse test database DSN: %w", err)
	}

	name := cfg.ConnConfig.Database
	if name == "" || !strings.HasSuffix(name, "_test") {
		return fmt.Errorf("refusing schema reset on database %q: test database name must end in _test", name)
	}

	return nil
}

// Open connects to the test database and verifies connectivity.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if err := ValidateTestDSN(dsn); err != nil {
		return nil, err
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open test database: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping test database: %w", err)
	}

	return pool, nil
}

// Reset rolls every migration back and then forward again. The Down+Up pair
// runs while holding AdvisoryLockKey so concurrent test packages cannot
// interleave schema changes.
func Reset(ctx context.Context, dsn string) error {
	if err := ValidateTestDSN(dsn); err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect test database: %w", err)
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire schema lock connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, AdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire schema advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, AdvisoryLockKey)
	}()

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("open migrate: %w", err)
	}
	defer func() {
		_, _ = m.Close()
	}()

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("migrate down: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// Truncate clears mutable rows between tests while preserving seeded taxonomy
// tables (commodity_synonyms and commodity_brands).
func Truncate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx,
		`TRUNCATE previews, price_logs, nfces, products, supermarkets, users RESTART IDENTITY CASCADE`)
	if err != nil {
		return fmt.Errorf("truncate test database: %w", err)
	}
	return nil
}
