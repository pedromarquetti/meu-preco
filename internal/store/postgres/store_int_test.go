package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"meupreco/migrations"
)

// testStore is the migrated pool shared by every postgres-backed test. It is
// nil when MEUPRECO_TEST_DATABASE_URL is unset, in which case tests skip.
var testStore *Store

func TestMain(m *testing.M) {
	dsn := os.Getenv("MEUPRECO_TEST_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "store: MEUPRECO_TEST_DATABASE_URL unset; skipping postgres integration tests")
		os.Exit(0)
	}

	if err := resetSchema(dsn); err != nil {
		fmt.Fprintln(os.Stderr, "store: reset schema:", err)
		os.Exit(1)
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store: connect:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := pool.Ping(ctx); err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, "store: ping:", err)
		os.Exit(1)
	}
	cancel()

	testStore = NewFromPool(pool)

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// schemaLockKey serializes schema-affecting setup against other test packages
// sharing the same MEUPRECO_TEST_DATABASE_URL (the httpapi integration suite
// uses the same key). Down+Up and Up-only migrations must never interleave.
const schemaLockKey int64 = 0x6d6575707265636f // "meupreco" as a u64

// resetSchema rolls every migration back then forward so a test run always
// starts from the same migrated state.
func resetSchema(dsn string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return err
	}

	if err := withSchemaLock(dsn, func() error {
		if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// withSchemaLock runs fn while holding the shared schema advisory lock on the
// same database. pg_advisory_lock is session-scoped, so the lock lives on a
// dedicated connection through the call.
func withSchemaLock(dsn string, fn func() error) (err error) {
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	conn, err := pool.Acquire(context.Background())
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(context.Background(),
		`SELECT pg_advisory_lock($1)`, schemaLockKey); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.Background(),
			`SELECT pg_advisory_unlock($1)`, schemaLockKey)
	}()

	return fn()
}

// mustStore returns the shared store, skipping the test when postgres is not
// configured.
func mustStore(t *testing.T) *Store {
	t.Helper()
	if testStore == nil {
		t.Skip("MEUPRECO_TEST_DATABASE_URL not set; skipping postgres integration test")
	}
	return testStore
}

// truncate clears user-dependent tables between tests. commodity_synonyms is
// deliberately kept: it holds the production seed installed by migration
// 000002, and the seeded taxonomy must survive across the suite.
func truncate(t *testing.T) {
	t.Helper()
	_, err := testStore.pool.Exec(context.Background(),
		`TRUNCATE previews, price_logs, nfces, products, supermarkets, users RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// jsonEqual compares two JSON documents semantically (JSONB reorders keys and
// whitespace, so raw-byte comparison would flake).
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}
	return reflect.DeepEqual(va, vb)
}
