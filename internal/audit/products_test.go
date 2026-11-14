package audit

import (
	"context"
	"testing"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

func TestAnalyzeClassifiesDriftAndCollision(t *testing.T) {
	arrozStem, arrozKey := product.CanonicalIdentity("Arroz Integral", "", "KG", nil)
	tijoloKey := product.CommodityKey("tijolo", "KG", "")

	products := []domain.Product{
		// Barcode identity: canonical_stem is optional and must never be
		// "repaired" into a commodity hash.
		{ID: 1, ProductKey: "07896008038717", GTIN: strPtr("07896008038717"), Name: "ARROZ TIO JOAO", Unit: "UN"},
		// Commodity row with no stem and a stale key. Its expected key is
		// already owned by product 3, so repairing it must be refused.
		{ID: 2, ProductKey: "old-key", Name: "Arroz Integral", Unit: "KG"},
		// Clean commodity row.
		{ID: 3, ProductKey: arrozKey, CanonicalStem: &arrozStem, Name: "Arroz Integral", Unit: "KG"},
		// Renamed commodity row: the key still matches the stored "tijolo"
		// stem, so only the stem itself is stale.
		{ID: 4, ProductKey: tijoloKey, CanonicalStem: strPtr("tijolo"), Name: "Apresuntado", Unit: "KG"},
	}

	findings := Analyze(products, nil)
	if len(findings) != len(products) {
		t.Fatalf("findings = %d, want %d", len(findings), len(products))
	}

	if got := findings[0]; len(got.Statuses) != 0 {
		t.Errorf("barcode product statuses = %v, want none", got.Statuses)
	}
	if got := findings[1]; !hasStatus(got.Statuses, MissingStem) ||
		!hasStatus(got.Statuses, KeyMismatch) ||
		!hasStatus(got.Statuses, IdentityCollision) ||
		got.CollisionWith != 3 {
		t.Errorf("product 2 finding = %+v, want missing stem + key mismatch + collision with 3", got)
	}
	if got := findings[2]; len(got.Statuses) != 0 {
		t.Errorf("clean product statuses = %v, want none", got.Statuses)
	}
	if got := findings[3]; !hasStatus(got.Statuses, StemMismatch) || hasStatus(got.Statuses, KeyMismatch) {
		t.Errorf("renamed product statuses = %v, want stem mismatch only", got.Statuses)
	}
}

func TestApplyRepairsNonCollidingRowsAndSkipsConflicts(t *testing.T) {
	_, key := product.CanonicalIdentity("Arroz Integral", "", "KG", nil)
	cleanStem, cleanKey := product.CanonicalIdentity("Feijao", "", "KG", nil)

	products := []domain.Product{
		{ID: 1, ProductKey: "stale", Name: "Arroz Integral", Unit: "KG"},
		{ID: 2, ProductKey: "stale-two", Name: "Arroz Integral", Unit: "KG"},
		{ID: 3, ProductKey: cleanKey, CanonicalStem: &cleanStem, Name: "Feijao", Unit: "KG"},
		{ID: 4, ProductKey: "concurrent", Name: "Outro Produto", Unit: "KG"},
	}

	stores := newFakeStores(products)
	stores.products.conflict[4] = true

	findings := Analyze(products, nil)
	if findings[1].CollisionWith != 1 {
		t.Fatalf("fixture collision = %d, want 1", findings[1].CollisionWith)
	}

	result, err := Apply(context.Background(), stores, findings)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Repaired != 1 {
		t.Errorf("repaired = %d, want 1", result.Repaired)
	}
	if got := skipIDs(result.Skipped); len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Errorf("skipped = %v, want [2 4]", got)
	}
	if got := stores.products.rows[1].ProductKey; got != key {
		t.Errorf("product 1 key = %q, want %q", got, key)
	}
	if got := stores.products.rows[2].ProductKey; got != "stale-two" {
		t.Errorf("colliding product was rewritten: key = %q", got)
	}

	// Re-auditing the repaired row finds no drift, and a second apply is a
	// no-op for it.
	again := Analyze(stores.snapshot(), nil)
	if again[0].Drifted() {
		t.Errorf("repaired product still drifts: %+v", again[0])
	}
	if _, err := Apply(context.Background(), stores, again); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(stores.products.updated) != 1 || stores.products.updated[0] != 1 {
		t.Errorf("identity updates = %v, want [1]", stores.products.updated)
	}
}

// TestAnalyzeKnownIncidentRows pins the two rows from the production incident:
// product 8 has the right name and a stale stem (safe to repair), while product
// 2 has a packaging-only name and a meaningful stem (manual review only).
func TestAnalyzeKnownIncidentRows(t *testing.T) {
	products := []domain.Product{
		{ID: 2, ProductKey: product.CommodityKey("laranja lima", "KG", ""), CanonicalStem: strPtr("laranja lima"), Name: "Saco", Unit: "KG"},
		{ID: 8, ProductKey: product.CommodityKey("tijolo", "KG", ""), CanonicalStem: strPtr("tijolo"), Name: "Apresuntado", Unit: "KG"},
	}

	findings := Analyze(products, nil)
	if got := findings[0]; !hasStatus(got.Statuses, StemMismatch) || !got.ManualReview {
		t.Errorf("product 2 finding = %+v, want stem mismatch + manual review", got)
	}
	if got := findings[1]; !hasStatus(got.Statuses, StemMismatch) || got.ManualReview {
		t.Errorf("product 8 finding = %+v, want repairable stem mismatch", got)
	}

	stores := newFakeStores(products)
	result, err := Apply(context.Background(), stores, findings)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Repaired != 1 || len(result.Skipped) != 1 || result.Skipped[0].ID != 2 {
		t.Errorf("apply = %+v, want only product 8 repaired", result)
	}
	if _, key := product.CanonicalIdentity("Apresuntado", "", "KG", nil); stores.products.rows[8].ProductKey != key {
		t.Errorf("product 8 key = %q, want %q", stores.products.rows[8].ProductKey, key)
	}
}

func TestAnalyzePackagingOnlyRowWithCoherentFallbackIsClean(t *testing.T) {
	stem, key := product.CanonicalIdentity("Saco", "", "KG", nil)
	p := domain.Product{ID: 1, ProductKey: key, CanonicalStem: &stem, Name: "Saco", Unit: "KG"}

	findings := Analyze([]domain.Product{p}, nil)
	if findings[0].Drifted() || findings[0].ManualReview {
		t.Errorf("coherent fallback identity = %+v, want no drift", findings[0])
	}
}

func skipIDs(skips []Skip) []int64 {
	out := make([]int64, 0, len(skips))
	for _, skip := range skips {
		out = append(out, skip.ID)
	}
	return out
}

// fakeStores implements just enough of domain.Stores for Apply: Tx runs the
// callback against the same fake and Products exposes the identity writer.
type fakeStores struct {
	domain.Stores
	products *fakeProducts
}

func newFakeStores(products []domain.Product) *fakeStores {
	rows := make(map[int64]*domain.Product, len(products))
	for i := range products {
		rows[products[i].ID] = &products[i]
	}
	return &fakeStores{products: &fakeProducts{rows: rows, conflict: map[int64]bool{}}}
}

func (f *fakeStores) Tx(_ context.Context, fn func(domain.Stores) error) error {
	return fn(f)
}

func (f *fakeStores) Products() domain.ProductStore { return f.products }

func (f *fakeStores) snapshot() []domain.Product {
	out := make([]domain.Product, 0, len(f.products.rows))
	for _, p := range f.products.rows {
		out = append(out, *p)
	}
	return out
}

type fakeProducts struct {
	domain.ProductStore
	rows     map[int64]*domain.Product
	conflict map[int64]bool
	updated  []int64
}

func (f *fakeProducts) UpdateIdentity(_ context.Context, id int64, stem, key string) error {
	if f.conflict[id] {
		return domain.ErrConflict
	}
	p, ok := f.rows[id]
	if !ok {
		return domain.ErrNotFound
	}
	p.CanonicalStem = &stem
	p.ProductKey = key
	f.updated = append(f.updated, id)
	return nil
}

func hasStatus(statuses []string, want string) bool {
	for _, status := range statuses {
		if status == want {
			return true
		}
	}
	return false
}

func strPtr(s string) *string { return &s }
