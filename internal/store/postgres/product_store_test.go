package store

import (
	"context"
	"errors"
	"testing"

	"meupreco/internal/domain"
)

func TestProductStoreLifecycle(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	p := &domain.Product{
		ProductKey: "07896008038717", // 13-digit GTIN stored, padded to 14 on write
		GTIN:       stringPtr("7896008038717"),
		Name:       "ARROZ BRANCO TIPO 1 5KG",
		Brand:      stringPtr("TIO JOAO"),
		Unit:       "UN",
	}
	if _, err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == 0 {
		t.Fatal("create did not set ID")
	}
	if p.GTIN == nil || *p.GTIN != "07896008038717" {
		t.Errorf("gtin normalized = %v, want 14-digit 07896008038717", *p.GTIN)
	}

	byID, err := s.Products().GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.Name != p.Name {
		t.Errorf("name = %q", byID.Name)
	}

	byKey, err := s.Products().GetByKey(ctx, "07896008038717")
	if err != nil {
		t.Fatalf("get by key: %v", err)
	}
	if byKey.ID != p.ID {
		t.Errorf("get by key id = %d, want %d", byKey.ID, p.ID)
	}

	// GetByGTIN accepts the bare 13-digit barcode; the trigger normalizes.
	byGTIN, err := s.Products().GetByGTIN(ctx, "7896008038717")
	if err != nil {
		t.Fatalf("get by gtin: %v", err)
	}
	if byGTIN.ID != p.ID {
		t.Errorf("get by gtin id = %d, want %d", byGTIN.ID, p.ID)
	}
}

func TestProductStoreConflict(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	p := &domain.Product{ProductKey: "key-a", Name: "X", Unit: "UN"}
	if _, err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.Products().Create(ctx, &domain.Product{ProductKey: "key-a", Name: "Y", Unit: "UN"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate product_key = %v, want ErrConflict", err)
	}
}

// TestProductSearchTrgm proves the DoD case: pg_trgm accent/case-insensitive
// search, "limao" finds "Limão Tahiti" via the accent-stripped canonical stem.
func TestProductSearchTrgm(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	if _, err := s.Products().Create(ctx, &domain.Product{
		ProductKey:    "cm-limao-tahiti",
		Name:          "Limão Tahiti",
		CanonicalStem: stringPtr("limao tahiti"),
		Unit:          "UN",
	}); err != nil {
		t.Fatalf("create limao: %v", err)
	}
	if _, err := s.Products().Create(ctx, &domain.Product{
		ProductKey:    "cm-batata",
		Name:          "Batata Inglesa",
		CanonicalStem: stringPtr("batata inglesa"),
		Unit:          "KG",
	}); err != nil {
		t.Fatalf("create batata: %v", err)
	}

	hits, err := s.Products().Search(ctx, "limao", 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Name != "Limão Tahiti" {
		t.Fatalf("search 'limao' = %+v, want the Limão Tahiti product", hits)
	}

	// Case-insensitive: a capitalized query still matches.
	hits, err = s.Products().Search(ctx, "BATATA", 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 || hits[0].Name != "Batata Inglesa" {
		t.Fatalf("search 'BATATA' = %+v", hits)
	}
}

func TestProductNotFound(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	if _, err := s.Products().GetByGTIN(ctx, "00000000000000"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.Products().GetByKey(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestProductUpsertMany(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	gtin := "07896008038717"
	rows := []*domain.Product{
		{ProductKey: gtin, GTIN: stringPtr("7896008038717"), Name: "ARROZ BRANCO TIPO 1 5KG", Unit: "UN"},
		{ProductKey: "cm-limao", Name: "Limão Tahiti", CanonicalStem: stringPtr("limao tahiti"), Unit: "UN"},
	}
	created, err := s.Products().UpsertMany(ctx, rows)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if created != 2 {
		t.Errorf("created = %d, want 2", created)
	}
	// GTIN normalized by the trigger.
	byG, err := s.Products().GetByGTIN(ctx, "7896008038717")
	if err != nil {
		t.Fatalf("get by gtin after upsert: %v", err)
	}
	if byG.Name != "ARROZ BRANCO TIPO 1 5KG" {
		t.Errorf("name = %q", byG.Name)
	}

	// Re-run: upserts in place (no duplicates), returns created=0.
	byG.Name = "ARROZ BRANCO TIPO 1 5KG (ATUALIZADO)"
	rows2 := []*domain.Product{{ProductKey: gtin, GTIN: stringPtr("7896008038717"), Name: byG.Name, Unit: "UN"}}
	created, err = s.Products().UpsertMany(ctx, rows2)
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if created != 0 {
		t.Errorf("created on re-run = %d, want 0 (idempotent)", created)
	}
	got, err := s.Products().GetByKey(ctx, gtin)
	if err != nil {
		t.Fatalf("get by key: %v", err)
	}
	if got.Name != "ARROZ BRANCO TIPO 1 5KG (ATUALIZADO)" {
		t.Errorf("name after re-upsert = %q", got.Name)
	}
}

func TestProductUpsertManyChunked(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	// Past chunkSize (100) to exercise the multi-statement path.
	rows := make([]*domain.Product, 0, 250)
	for i := 0; i < 250; i++ {
		rows = append(rows, &domain.Product{
			ProductKey: "key-" + string(rune('a'+i%26)) + itoa(i),
			Name:       "Produto",
			Unit:       "UN",
		})
	}
	created, err := s.Products().UpsertMany(ctx, rows)
	if err != nil {
		t.Fatalf("upsert many: %v", err)
	}
	if created != 250 {
		t.Errorf("created = %d, want 250", created)
	}
}

func TestProductNearestMatch(t *testing.T) {
	s := mustStore(t)
	truncate(t)
	ctx := context.Background()

	if _, err := s.Products().Create(ctx, &domain.Product{
		ProductKey:    "cm-batata",
		Name:          "Batata Inglesa",
		CanonicalStem: stringPtr("batata inglesa"),
		Unit:          "KG",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	p, sim, err := s.Products().NearestMatch(ctx, "batata", 0.3)
	if err != nil {
		t.Fatalf("nearest match: %v", err)
	}
	if sim < 0.3 {
		t.Errorf("sim = %f, below threshold", sim)
	}
	if p == nil || p.Name != "Batata Inglesa" {
		t.Fatalf("product = %+v", p)
	}

	// A query with nothing related must not match anything.
	if _, _, err := s.Products().NearestMatch(ctx, "zzzzzzzzzzzzzzz", 0.3); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func stringPtr(s string) *string { return &s }
