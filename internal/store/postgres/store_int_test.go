package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"meupreco/internal/testdb"
)

// testStore is the migrated store shared by every postgres-backed test.
var testStore *Store

func TestMain(m *testing.M) {
	dsn, err := testdb.RequireDSN()
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	if err := testdb.Reset(ctx, dsn); err != nil {
		fmt.Fprintln(os.Stderr, "store: reset schema:", err)
		os.Exit(1)
	}

	pool, err := testdb.Open(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		os.Exit(1)
	}
	testStore = NewFromPool(pool)

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// truncate clears user-dependent tables between tests. commodity_synonyms is
// deliberately kept: it holds the production seed installed by migration
// 000002, and the seeded taxonomy must survive across the suite.
func truncate(t *testing.T) {
	t.Helper()
	if err := testdb.Truncate(context.Background(), testStore.pool); err != nil {
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
