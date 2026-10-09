package enrich

import (
	"bytes"
	"compress/gzip"
	"context"
	"sync"
	"testing"

	"meupreco/internal/domain"
)

// memSeedStore records upserts so tests can assert idempotency.
type memSeedStore struct {
	mu     sync.Mutex
	seen   map[string]*domain.Product
	upsert int
}

func newMemSeedStore() *memSeedStore {
	return &memSeedStore{seen: make(map[string]*domain.Product)}
}

func (m *memSeedStore) UpsertMany(_ context.Context, products []*domain.Product) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upsert++
	created := 0
	for _, p := range products {
		if _, ok := m.seen[p.ProductKey]; !ok {
			created++
		}
		m.seen[p.ProductKey] = p
	}
	return created, nil
}

func gzipJSONL(t *testing.T, lines []string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	for _, l := range lines {
		if _, err := gw.Write([]byte(l + "\n")); err != nil {
			t.Fatalf("write line: %v", err)
		}
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return &buf
}

// Two valid 789 GTINs (check digits verified), one bad-prefix and one
// check-digit-broken line the importer must skip.
var seedLines = []string{
	`{"code":"7896008038717","product_name":"Pão de Forma Integral 500g","brands":"Pullman","quantity":"500 g"}`,
	`{"code":"7896004021522","product_name":"Arroz Branco Tipo 1 5kg","brands":"Camil","quantity":"5 kg"}`,
	`{"code":"5901234123457","product_name":"Something European","quantity":"250 g"}`,
	`{"code":"7896008038713","product_name":"Bad check digit","quantity":"1 kg"}`,
	`{"code":"","product_name":"No code at all"}`,
}

func TestSeedFromOFFUpserts(t *testing.T) {
	store := newMemSeedStore()
	stats, err := SeedFromOFF(context.Background(), store, gzipJSONL(t, seedLines))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if stats.LinesRead != 5 {
		t.Errorf("lines read = %d, want 5", stats.LinesRead)
	}
	if stats.GTINValid != 2 {
		t.Errorf("valid gtin count = %d, want 2", stats.GTINValid)
	}
	if stats.RowsUpserted != 2 || stats.Created != 2 {
		t.Errorf("rows=%d created=%d, want 2/2", stats.RowsUpserted, stats.Created)
	}
	if len(store.seen) != 2 {
		t.Fatalf("stored products = %d, want 2", len(store.seen))
	}
	p := store.seen["07896008038717"]
	if p == nil || p.Name != "Pão de Forma Integral 500g" || p.Unit != "GR" {
		t.Errorf("stored bread = %+v", p)
	}
	if p.Brand == nil || *p.Brand != "Pullman" {
		t.Errorf("brand = %+v", p.Brand)
	}
}

func TestSeedFromOFFIdempotent(t *testing.T) {
	store := newMemSeedStore()
	ctx := context.Background()

	if _, err := SeedFromOFF(ctx, store, gzipJSONL(t, seedLines)); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	stats2, err := SeedFromOFF(ctx, store, gzipJSONL(t, seedLines))
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if stats2.Created != 0 {
		t.Errorf("second run created = %d, want 0 (idempotent)", stats2.Created)
	}
	if stats2.RowsUpserted != 2 {
		t.Errorf("second run rows = %d, want 2 (still upserted)", stats2.RowsUpserted)
	}
	if len(store.seen) != 2 {
		t.Errorf("total stored = %d, want 2 (no duplicates)", len(store.seen))
	}
}
