package store

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

func TestProductStoreLifecycle(t *testing.T) {
	s := testStore
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
	sm := seedSupermarket(t, s)
	n := seedNfce(t, s, sm)
	if err := s.PriceLogs().Create(ctx, &domain.PriceLog{
		NfceID: n.ID, ProductID: &p.ID, Line: 1, DescriptionSnapshot: p.Name,
		Qty: decimal.NewFromInt(1), Unit: p.Unit, UnitPrice: decimal.NewFromInt(10),
		TotalPrice: decimal.NewFromInt(10), IdentityStatus: "gtin_known",
	}); err != nil {
		t.Fatalf("create approved log: %v", err)
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

// TestProductSearchTrgm proves the DoD case: pg_trgm accent/case-insensitive
// search, "limao" finds "Limão Tahiti" via the accent-stripped canonical stem.
func TestProductSearchTrgm(t *testing.T) {
	s := testStore
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
	s := testStore
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
	s := testStore
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
	s := testStore
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
	s := testStore
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

func TestProductVisibility(t *testing.T) {
	store := testStore
	truncate(t)
	ctx := context.Background()
	sm := seedSupermarket(t, store)
	nfce := seedNfce(t, store, sm)

	addLog := func(t *testing.T, productID *int64, line int, pending bool) {
		t.Helper()
		l := &domain.PriceLog{
			NfceID: nfce.ID, ProductID: productID, Line: line,
			DescriptionSnapshot: "test price", StoreCode: "STORE",
			Qty: decimal.NewFromInt(1), Unit: "UN",
			UnitPrice: decimal.NewFromInt(10), TotalPrice: decimal.NewFromInt(10),
			IdentityStatus: "gtin_known", NeedsReview: pending,
		}
		if err := store.PriceLogs().Create(ctx, l); err != nil {
			t.Fatalf("create price log: %v", err)
		}
	}

	// Scenario A: No logs -> Excluded from search and 404 on lookup.
	pA := &domain.Product{
		ProductKey: "test-no-logs",
		Name:       "No Logs Product",
		Unit:       "UN",
	}
	_, err := store.Products().Create(ctx, pA)
	if err != nil {
		t.Fatalf("create pA: %v", err)
	}

	hits, _, _ := store.Products().SearchFiltered(ctx, domain.ProductFilter{Query: "no-logs"}, 10, 0)
	if len(hits) > 0 {
		t.Errorf("Scenario A failed: product with no logs found in search")
	}

	_, err = store.Products().GetByID(ctx, pA.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Scenario A failed: expected ErrNotFound for lookup, got %v", err)
	}

	// Scenario B: Pending Only -> Hidden from public view.
	pB := &domain.Product{
		ProductKey:  "test-pending-only",
		Name:        "Pending Product",
		Unit:        "UN",
		NeedsReview: true,
	}
	if _, err := store.Products().Create(ctx, pB); err != nil {
		t.Fatalf("create pB: %v", err)
	}

	addLog(t, &pB.ID, 1, true)

	hits, _, _ = store.Products().SearchFiltered(ctx, domain.ProductFilter{Query: "pending-product"}, 10, 0)
	if len(hits) > 0 {
		t.Errorf("Scenario B failed: product with only pending logs found in search")
	}
	adminHits, adminTotal, err := store.Products().SearchAdminFiltered(ctx, domain.ProductFilter{Query: "pending-product"}, 10, 0)
	if err != nil {
		t.Fatalf("admin search pending product: %v", err)
	}
	if adminTotal != 2 || len(adminHits) != 2 {
		t.Errorf("admin search = %+v (total %d), want pending and no-log products", adminHits, adminTotal)
	}
	var foundPending bool
	for _, hit := range adminHits {
		foundPending = foundPending || hit.ID == pB.ID
	}
	if !foundPending {
		t.Errorf("admin search = %+v, missing pending-only product %d", adminHits, pB.ID)
	}

	_, err = store.Products().GetByID(ctx, pB.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Scenario B failed: expected ErrNotFound for lookup of pending-only product, got %v", err)
	}

	// Scenario C: Approved Only -> Visible in search and history.
	pC := &domain.Product{
		ProductKey: "test-approved-only",
		Name:       "Approved Product",
		Unit:       "UN",
	}
	if _, err := store.Products().Create(ctx, pC); err != nil {
		t.Fatalf("create pC: %v", err)
	}

	addLog(t, &pC.ID, 2, false)

	hits, _, _ = store.Products().SearchFiltered(ctx, domain.ProductFilter{Query: "approved-product"}, 10, 0)
	if len(hits) == 0 || hits[0].ID != pC.ID {
		t.Errorf("Scenario C failed: product with approved log not found in search")
	}

	_, err = store.Products().GetByID(ctx, pC.ID)
	if err != nil {
		t.Fatalf("Scenario C failed: expected success for lookup of approved-only product, got %v", err)
	}

	// Scenario D: Mixed Logs -> Visible to the public (presence of an approval "unlocks" visibility).
	pD := &domain.Product{
		ProductKey: "test-mixed-logs",
		Name:       "Mixed Product",
		Unit:       "UN",
	}
	if _, err := store.Products().Create(ctx, pD); err != nil {
		t.Fatalf("create pD: %v", err)
	}

	addLog(t, &pD.ID, 3, false)
	addLog(t, &pD.ID, 4, true)

	hits, _, _ = store.Products().SearchFiltered(ctx, domain.ProductFilter{Query: "mixed-product"}, 10, 0)
	if len(hits) == 0 || hits[0].ID != pD.ID {
		t.Errorf("Scenario D failed: product with mixed logs not found in search")
	}

	// Scenario E: Search Totals match the filtered result page.
	pE1 := &domain.Product{ProductKey: "e1", Name: "Prod 1", Unit: "UN"}
	if _, err := store.Products().Create(ctx, pE1); err != nil {
		t.Fatalf("create pE1: %v", err)
	}
	addLog(t, &pE1.ID, 5, false)

	pE2 := &domain.Product{ProductKey: "e2", Name: "Prod 2", Unit: "UN"}
	if _, err := store.Products().Create(ctx, pE2); err != nil {
		t.Fatalf("create pE2: %v", err)
	}
	addLog(t, &pE2.ID, 6, false)

	pE3 := &domain.Product{ProductKey: "e3", Name: "Prod 3 (Pending)", Unit: "UN"}
	if _, err := store.Products().Create(ctx, pE3); err != nil {
		t.Fatalf("create pE3: %v", err)
	}
	addLog(t, &pE3.ID, 7, true)

	// The approved products (C, D, E1, and E2) are visible; E3 is excluded.
	hits, total, err := store.Products().SearchFiltered(ctx, domain.ProductFilter{}, 10, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if len(hits) != 4 {
		t.Errorf("Scenario E failed: expected 4 approved-product hits, got %d", len(hits))
	}
	if total != 4 {
		t.Errorf("Scenario E failed: expected count of 4, got %d", total)
	}

	// Verify that the search results are correct (E1 and E2 only)
	for _, h := range hits {
		if h.ProductKey == "e3" {
			t.Errorf("Scenario E failed: found pending product in result")
		}
	}
}
