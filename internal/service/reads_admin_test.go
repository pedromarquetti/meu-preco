package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
	"meupreco/internal/product"
)

// nfceSeq hands out unique access keys across seeds so a shared memStores never
// collides (mirroring the DB's UNIQUE(access_key)).
var nfceSeq atomic.Int64

func nextNfceKey() string {
	n := nfceSeq.Add(1)
	return fmt.Sprintf("35%018d", n)
}

// seedOpenProduct installs a review-clean product (optionally with a GTIN).
func seedOpenProduct(t *testing.T, m *memStores, name, unit string, gtin *string) *domain.Product {
	t.Helper()
	key := name
	if gtin != nil {
		key = *gtin
	}
	p := &domain.Product{ProductKey: key, Name: name, Unit: unit, GTIN: gtin}
	if err := m.Products().Create(context.Background(), p); err != nil {
		t.Fatalf("seed product %q: %v", name, err)
	}
	return p
}

// seedStoreAndLog installs a supermarket and a receipt with one logged line for
// product p. The price_per_unit is derived as total/qty so assertions can rely
// on the fold math.
func seedStoreAndLog(t *testing.T, m *memStores, sm *domain.Supermarket, p *domain.Product, unit string, qty, total decimal.Decimal, issuedAt time.Time) *domain.PriceLog {
	t.Helper()
	if err := m.Supermarkets().Upsert(context.Background(), sm); err != nil {
		t.Fatalf("seed supermarket: %v", err)
	}
	issued := issuedAt
	nf := &domain.Nfce{
		AccessKey:   nextNfceKey(),
		Supermarket: *sm,
		IssuedAt:    &issued,
		CreatedBy:   int64Ptr(1),
	}
	if err := m.Nfces().Create(context.Background(), nf); err != nil {
		t.Fatalf("seed nfce: %v", err)
	}
	ppu := total.Div(qty)
	l := &domain.PriceLog{
		NfceID:              nf.ID,
		ProductID:           &p.ID,
		Line:                1,
		DescriptionSnapshot: p.Name,
		StoreCode:           "000100",
		Qty:                 qty,
		Unit:                unit,
		NormalizedUnit:      strPtr(unit),
		PricePerUnit:        &ppu,
		UnitPrice:           total.Div(qty),
		TotalPrice:          total,
		IdentityStatus:      product.StatusGtinKnown,
	}
	if err := m.PriceLogs().Create(context.Background(), l); err != nil {
		t.Fatalf("seed price_log: %v", err)
	}
	return l
}

func int64Ptr(v int64) *int64 { return &v }

func mustDecimal(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestSearchProductsFiltersAndExcludesReview(t *testing.T) {
	m := newMemStores()
	p1 := seedOpenProduct(t, m, "ARROZ BRANCO CAMIL", "KG", nil)
	seedOpenProduct(t, m, "ARROZ INTEGRAL BIO", "KG", nil)
	p3 := seedOpenProduct(t, m, "POLPA DE FRUTA", "GR", strPtr("07891000412855"))
	seedOpenProduct(t, m, "CAFE EXTRA FORTE", "UN", nil)
	review := &domain.Product{ProductKey: "review", Name: "ARROZ ORGANICO", Unit: "KG", NeedsReview: true}
	if err := m.Products().Create(context.Background(), review); err != nil {
		t.Fatalf("seed review product: %v", err)
	}

	svc := newTestService(m)

	// The needs_review row must never surface, whatever the filter.
	page, err := svc.SearchProducts(context.Background(), domain.ProductFilter{Query: "arroz"}, 1, 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Items) != 2 {
		t.Errorf("arroz items = %d, want 2 (review hidden): %+v", len(page.Items), page.Items)
	}
	if page.Page.Total != 2 || page.Page.TotalPages != 1 {
		t.Errorf("page = %+v", page.Page)
	}

	page, err = svc.SearchProducts(context.Background(), domain.ProductFilter{Brand: "CAMIL"}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != p1.ID {
		t.Errorf("brand items = %+v, want %d", page.Items, p1.ID)
	}

	page, err = svc.SearchProducts(context.Background(), domain.ProductFilter{GTIN: "789"}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != p3.ID {
		t.Errorf("gtin items = %+v, want %d", page.Items, p3.ID)
	}

	// Units are normalized before the store is hit: "kg" matches KG rows.
	page, err = svc.SearchProducts(context.Background(), domain.ProductFilter{Unit: "kg"}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Errorf("unit kg items = %d, want 2 (both ARROZ rows)", len(page.Items))
	}
}

func TestSearchProductsPaginates(t *testing.T) {
	m := newMemStores()
	for i := 0; i < 5; i++ {
		seedOpenProduct(t, m, fmt.Sprintf("PRODUTO %d", i), "UN", nil)
	}
	svc := newTestService(m)

	page, err := svc.SearchProducts(context.Background(), domain.ProductFilter{}, 2, 2)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Items) != 2 {
		t.Errorf("page items = %d, want 2", len(page.Items))
	}
	if page.Page.Page != 2 || page.Page.Limit != 2 || page.Page.Total != 5 || page.Page.TotalPages != 3 {
		t.Errorf("page = %+v", page.Page)
	}
}

func TestSearchProductsInvalidFilters(t *testing.T) {
	m := newMemStores()
	seedOpenProduct(t, m, "CAFE EXTRA FORTE", "UN", nil)
	svc := newTestService(m)

	cases := []struct {
		name string
		f    domain.ProductFilter
		page int
		lim  int
	}{
		{"bad gtin letters", domain.ProductFilter{GTIN: "78ab"}, 1, 20},
		{"gtin too long", domain.ProductFilter{GTIN: "123456789012345"}, 1, 20},
		{"unknown unit", domain.ProductFilter{Unit: "LITERS"}, 1, 20},
		{"page zero", domain.ProductFilter{}, 0, 20},
		{"limit zero", domain.ProductFilter{}, 1, 0},
		{"limit too big", domain.ProductFilter{}, 1, 101},
	}
	for _, tc := range cases {
		if _, err := svc.SearchProducts(context.Background(), tc.f, tc.page, tc.lim); !errors.Is(err, ErrInvalidFilter) {
			t.Errorf("%s: err = %v, want ErrInvalidFilter", tc.name, err)
		}
	}
}

func TestGetProductNotFound(t *testing.T) {
	svc := newTestService(newMemStores())
	if _, err := svc.GetProduct(context.Background(), 404); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("err = %v, want ErrProductNotFound", err)
	}
}

func TestProductHistoryMixedUnitsFold(t *testing.T) {
	m := newMemStores()
	p := seedOpenProduct(t, m, "OVOS CAIPIRA", "UN", nil)
	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// A per-piece UN point at 0.50.
	seedStoreAndLog(t, m, sm, p, "UN", mustDecimal("2"), mustDecimal("1.00"), base)
	// A per-dozen DZ point at 6.00 (folds to 0.50 per piece).
	seedStoreAndLog(t, m, sm, p, "DZ", mustDecimal("1"), mustDecimal("6.00"), base.Add(24*time.Hour))
	// A per-dozen DZ point at 12.00 (folds to 1.00 per piece) — newest.
	seedStoreAndLog(t, m, sm, p, "DZ", mustDecimal("1"), mustDecimal("12.00"), base.Add(48*time.Hour))

	svc := newTestService(m)
	h, err := svc.ProductHistory(context.Background(), p.ID, nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(h.Points) != 3 {
		t.Fatalf("points = %d, want 3", len(h.Points))
	}

	// Ordered by issued_at, so the last point is the newest DZ@12.
	last := h.Points[len(h.Points)-1]
	if last.ComparableUnit != "UN" || last.ComparablePricePerUnit == nil {
		t.Fatalf("last point = %+v, want a UN-folded comparable price", last)
	}
	if got := decimal.RequireFromString(*last.ComparablePricePerUnit); !got.Equal(mustDecimal("1")) {
		t.Errorf("last comparable price = %s, want 1 (12.00/12)", got)
	}

	// One aggregate series (UN) with count 3, min 0.5, max 1.0, avg 2/3, last 1.
	if len(h.Aggregate) != 1 {
		t.Fatalf("aggregate = %+v, want a single UN series", h.Aggregate)
	}
	agg := h.Aggregate[0]
	if agg.Unit != "UN" || agg.Count != 3 {
		t.Errorf("aggregate series = %+v", agg)
	}
	if got := decimal.RequireFromString(*agg.Min); !got.Equal(mustDecimal("0.5")) {
		t.Errorf("min = %s, want 0.5", got)
	}
	if got := decimal.RequireFromString(*agg.Max); !got.Equal(mustDecimal("1")) {
		t.Errorf("max = %s, want 1", got)
	}
	if got := decimal.RequireFromString(*agg.Avg); !got.Equal(mustDecimal("2").Div(mustDecimal("3"))) {
		t.Errorf("avg = %s, want 2/3", got)
	}
	if got := decimal.RequireFromString(*agg.Last); !got.Equal(mustDecimal("1")) {
		t.Errorf("last = %s, want 1", got)
	}
}

func TestProductHistoryWindowAndNotFound(t *testing.T) {
	m := newMemStores()
	p := seedOpenProduct(t, m, "OVOS CAIPIRA", "UN", nil)
	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO"}
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedStoreAndLog(t, m, sm, p, "UN", mustDecimal("1"), mustDecimal("2.00"), base)
	seedStoreAndLog(t, m, sm, p, "UN", mustDecimal("1"), mustDecimal("3.00"), base.Add(7*24*time.Hour))
	svc := newTestService(m)

	from := base
	to := base.Add(2 * 24 * time.Hour)
	h, err := svc.ProductHistory(context.Background(), p.ID, &from, &to)
	if err != nil {
		t.Fatalf("windowed history: %v", err)
	}
	if len(h.Points) != 1 {
		t.Errorf("windowed points = %d, want 1", len(h.Points))
	}

	// A flipped window is a 400-class invalid filter.
	badFrom, badTo := to, from
	if _, err := svc.ProductHistory(context.Background(), p.ID, &badFrom, &badTo); !errors.Is(err, ErrInvalidFilter) {
		t.Errorf("flipped window err = %v, want ErrInvalidFilter", err)
	}
	if _, err := svc.ProductHistory(context.Background(), 404, nil, nil); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("err = %v, want ErrProductNotFound", err)
	}
}

func TestListSupermarketsAndProducts(t *testing.T) {
	m := newMemStores()
	p1 := seedOpenProduct(t, m, "ARROZ BRANCO", "KG", nil)
	p2 := seedOpenProduct(t, m, "CAFE EXTRA", "UN", nil)
	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO"}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedStoreAndLog(t, m, sm, p1, "KG", mustDecimal("1"), mustDecimal("4.00"), base)
	seedStoreAndLog(t, m, sm, p2, "UN", mustDecimal("2"), mustDecimal("9.00"), base.Add(-24*time.Hour))
	seedStoreAndLog(t, m, sm, p2, "UN", mustDecimal("1"), mustDecimal("4.50"), base)

	svc := newTestService(m)

	list, err := svc.ListSupermarkets(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("list supermarkets: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != sm.ID {
		t.Fatalf("supermarkets = %+v", list.Items)
	}

	page, err := svc.SupermarketProducts(context.Background(), sm.ID, 1, 20)
	if err != nil {
		t.Fatalf("supermarket products: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("store products = %+v, want 2", page.Items)
	}
	// p2 has two logs; the latest must win (4.50, not the 9.00 older one).
	for _, it := range page.Items {
		if it.Product.ID == p2.ID {
			if it.UnitPrice != "4.5" && it.UnitPrice != "4.50" {
				t.Errorf("p2 unit price = %s, want 4.5 (latest wins)", it.UnitPrice)
			}
		}
	}

	if _, err := svc.SupermarketProducts(context.Background(), 404, 1, 20); !errors.Is(err, ErrSupermarketNotFound) {
		t.Errorf("err = %v, want ErrSupermarketNotFound", err)
	}
}

func TestNeedsReviewQueue(t *testing.T) {
	m := newMemStores()
	p := &domain.Product{ProductKey: "known", Name: "LIMAO TAHITI", Unit: "KG", NeedsReview: true}
	if err := m.Products().Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO DO CENTRO"}
	if err := m.Supermarkets().Upsert(context.Background(), sm); err != nil {
		t.Fatal(err)
	}
	nf := &domain.Nfce{AccessKey: fmt.Sprintf("35%018d", 1), Supermarket: *sm}
	if err := m.Nfces().Create(context.Background(), nf); err != nil {
		t.Fatal(err)
	}
	ppu := mustDecimal("5.99")
	l := &domain.PriceLog{
		NfceID:              nf.ID,
		Line:                2,
		DescriptionSnapshot: "LIMAO TAHITI KG",
		StoreCode:           "000222",
		Qty:                 mustDecimal("1"),
		Unit:                "KG",
		PricePerUnit:        &ppu,
		UnitPrice:           mustDecimal("5.99"),
		TotalPrice:          mustDecimal("5.99"),
		NeedsReview:         true,
	}
	if err := m.PriceLogs().Create(context.Background(), l); err != nil {
		t.Fatal(err)
	}

	svc := newTestService(m)
	q, err := svc.NeedsReviewQueue(context.Background(), 1, 20)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(q.Items) != 1 {
		t.Fatalf("queue items = %d, want 1", len(q.Items))
	}
	it := q.Items[0]
	if it.PriceLogID != l.ID || it.NfceID != nf.ID || it.StoreCode != "000222" {
		t.Errorf("item = %+v", it)
	}
	if it.CNPJBase != "12345678" || it.StoreName != "MERCADO DO CENTRO" {
		t.Errorf("store context = %q/%q", it.CNPJBase, it.StoreName)
	}
	if it.SuggestedStem != "limao tahiti" {
		t.Errorf("suggested stem = %q, want limao tahiti", it.SuggestedStem)
	}
}

func TestBatchAssignExistingAndCreate(t *testing.T) {
	m := newMemStores()
	admin := &domain.User{Email: "admin@example.com", Role: "admin"}
	if err := m.Users().Create(context.Background(), admin); err != nil {
		t.Fatal(err)
	}

	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO"}
	mustReviewLogs(t, m, sm, 2)

	existing := &domain.Product{ProductKey: "banana", Name: "BANANA PRATA", Unit: "KG"}
	if err := m.Products().Create(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	logs := m.reviewLogs()

	res, err := newTestService(m).BatchAssign(context.Background(), admin.ID, BatchAssignRequest{
		Assignments: []Assignment{
			{ID: logs[0].ID, Action: "existing", ProductID: existing.ID},
			{ID: logs[1].ID, Action: "create", Name: "MACA FUJI", Brand: "", Unit: "KG"},
		},
	})
	if err != nil {
		t.Fatalf("batch assign: %v", err)
	}
	if res.Assigned != 2 || res.Created != 1 {
		t.Errorf("result = %+v, want 2 assigned, 1 created", res)
	}

	got0, _ := m.PriceLogs().GetByID(context.Background(), logs[0].ID)
	if got0.ProductID == nil || *got0.ProductID != existing.ID || got0.NeedsReview {
		t.Errorf("log 0 = %+v, want wired to existing product and cleared", got0)
	}
	if got0.ResolvedBy == nil || *got0.ResolvedBy != admin.ID || got0.ResolutionNote == nil || *got0.ResolutionNote != "existing" {
		t.Errorf("log 0 audit = resolved_by %v note %v, want admin/existing", got0.ResolvedBy, got0.ResolutionNote)
	}

	got1, _ := m.PriceLogs().GetByID(context.Background(), logs[1].ID)
	if got1.ProductID == nil || got1.NeedsReview {
		t.Fatalf("log 1 = %+v, want wired and cleared", got1)
	}
	created, err := m.Products().GetByID(context.Background(), *got1.ProductID)
	if err != nil {
		t.Fatalf("created product missing: %v", err)
	}
	if created.ProductKey != product.CommodityKey("maca fuji", "KG") {
		t.Errorf("created key = %q, want commodity hash", created.ProductKey)
	}
	if created.CreatedBy == nil || *created.CreatedBy != admin.ID {
		t.Errorf("created_by = %v, want admin", created.CreatedBy)
	}
}

func mustReviewLogs(t *testing.T, m *memStores, sm *domain.Supermarket, n int) []*domain.PriceLog {
	t.Helper()
	if err := m.Supermarkets().Upsert(context.Background(), sm); err != nil {
		t.Fatal(err)
	}
	var out []*domain.PriceLog
	for i := 0; i < n; i++ {
		nf := &domain.Nfce{AccessKey: nextNfceKey(), Supermarket: *sm}
		if err := m.Nfces().Create(context.Background(), nf); err != nil {
			t.Fatal(err)
		}
		l := &domain.PriceLog{
			NfceID: nf.ID, Line: 1, DescriptionSnapshot: "FRUTA AVULSA",
			Unit: "KG", Qty: mustDecimal("1"), TotalPrice: mustDecimal("1.00"),
			NeedsReview: true,
		}
		if err := m.PriceLogs().Create(context.Background(), l); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	return out
}

func (m *memStores) reviewLogs() []*domain.PriceLog {
	var out []*domain.PriceLog
	for _, l := range m.priceLogs {
		if l.NeedsReview {
			out = append(out, l)
		}
	}
	return out
}

func TestBatchAssignValidation(t *testing.T) {
	m := newMemStores()
	admin := &domain.User{Email: "admin@example.com", Role: "admin"}
	if err := m.Users().Create(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(m)

	cases := []struct {
		name string
		req  BatchAssignRequest
	}{
		{"empty", BatchAssignRequest{}},
		{"dup id", BatchAssignRequest{Assignments: []Assignment{{ID: 1, Action: "existing", ProductID: 1}, {ID: 1, Action: "existing", ProductID: 2}}}},
		{"unknown action", BatchAssignRequest{Assignments: []Assignment{{ID: 1, Action: "fire"}}}},
		{"existing no product", BatchAssignRequest{Assignments: []Assignment{{ID: 1, Action: "existing"}}}},
		{"create empty name", BatchAssignRequest{Assignments: []Assignment{{ID: 1, Action: "create", Unit: "KG"}}}},
		{"create bad unit", BatchAssignRequest{Assignments: []Assignment{{ID: 1, Action: "create", Name: "BANANA", Unit: "LITERS"}}}},
		{"missing target product", BatchAssignRequest{Assignments: []Assignment{{ID: 1, Action: "existing", ProductID: 999}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "X"}
			logs := mustReviewLogs(t, m, sm, 1)
			id := logs[0].ID
			_, err := svc.BatchAssign(context.Background(), admin.ID, tc.req)
			if !errors.Is(err, ErrInvalidAssignment) {
				t.Errorf("err = %v, want ErrInvalidAssignment", err)
			}
			// Rollback safety: nothing may have been written by the failed batch.
			got, _ := m.PriceLogs().GetByID(context.Background(), id)
			if got == nil || got.ProductID != nil || !got.NeedsReview {
				t.Errorf("log %d mutated by failed batch", id)
			}
		})
	}
}

func TestBatchAssignAtomicRollback(t *testing.T) {
	m := newMemStores()
	admin := &domain.User{Email: "admin@example.com", Role: "admin"}
	if err := m.Users().Create(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "X"}
	log := mustReviewLogs(t, m, sm, 1)[0]
	svc := newTestService(m)

	_, err := svc.BatchAssign(context.Background(), admin.ID, BatchAssignRequest{
		Assignments: []Assignment{
			{ID: log.ID, Action: "create", Name: "BANANA PRATA", Unit: "KG"},
			{ID: 777777, Action: "existing", ProductID: 999},
		},
	})
	if !errors.Is(err, ErrInvalidAssignment) {
		t.Fatalf("err = %v, want ErrInvalidAssignment", err)
	}
	got, _ := m.PriceLogs().GetByID(context.Background(), log.ID)
	if got.ProductID != nil || !got.NeedsReview {
		t.Errorf("log mutated despite rollback: %+v", got)
	}
	if _, err := m.Products().GetByKey(context.Background(), product.CommodityKey("banana prata", "KG")); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("created product survived a rolled-back batch: %v", err)
	}
}

func TestMergeProducts(t *testing.T) {
	m := newMemStores()
	from := seedOpenProduct(t, m, "ARROZ BRANCO CRISTAL", "KG", nil)
	to := seedOpenProduct(t, m, "ARROZ BRANCO", "KG", nil)
	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO"}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedStoreAndLog(t, m, sm, from, "KG", mustDecimal("1"), mustDecimal("4.00"), base)
	seedStoreAndLog(t, m, sm, from, "KG", mustDecimal("1"), mustDecimal("4.20"), base.Add(time.Hour))

	svc := newTestService(m)
	res, err := svc.MergeProducts(context.Background(), from.ID, to.ID)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.LogsMoved != 2 || !res.DupRemoved {
		t.Errorf("result = %+v", res)
	}
	if _, err := m.Products().GetByID(context.Background(), from.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("dup still present after merge")
	}
	if n := mustCountLogs(m, to.ID); n != 2 {
		t.Errorf("logs on canonical = %d, want 2", n)
	}

	if _, err := svc.MergeProducts(context.Background(), from.ID, from.ID); !errors.Is(err, ErrInvalidAssignment) {
		t.Errorf("self merge err = %v", err)
	}
	if _, err := svc.MergeProducts(context.Background(), 1, 999); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("missing target err = %v", err)
	}
}

func mustCountLogs(m *memStores, productID int64) int {
	n, _ := m.PriceLogs().CountByProduct(context.Background(), productID)
	return int(n)
}

func TestMergeSupermarkets(t *testing.T) {
	m := newMemStores()
	sm1 := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO"}
	sm2 := &domain.Supermarket{CNPJ: "12345678000289", LegalName: "MERCADO BAIRRO"}
	if err := m.Supermarkets().Upsert(context.Background(), sm1); err != nil {
		t.Fatal(err)
	}
	if err := m.Supermarkets().Upsert(context.Background(), sm2); err != nil {
		t.Fatal(err)
	}
	nf := &domain.Nfce{AccessKey: "35" + repeat("1", 42), Supermarket: *sm1}
	if err := m.Nfces().Create(context.Background(), nf); err != nil {
		t.Fatal(err)
	}

	svc := newTestService(m)
	res, err := svc.MergeSupermarkets(context.Background(), sm1.CNPJ, sm2.CNPJ)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.NfcesMoved != 1 || !res.DupRemoved {
		t.Errorf("result = %+v", res)
	}
	got, _ := m.Nfces().GetByAccessKey(context.Background(), nf.AccessKey)
	if got.Supermarket.ID != sm2.ID {
		t.Errorf("receipt supermarket = %d, want %d", got.Supermarket.ID, sm2.ID)
	}
	if _, err := m.Supermarkets().GetByCNPJ(context.Background(), sm1.CNPJ); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("dup store still present after merge")
	}

	if _, err := svc.MergeSupermarkets(context.Background(), sm1.CNPJ, sm1.CNPJ); !errors.Is(err, ErrInvalidAssignment) {
		t.Errorf("self merge err = %v", err)
	}
	if _, err := svc.MergeSupermarkets(context.Background(), "00000000000000", sm2.CNPJ); !errors.Is(err, ErrSupermarketNotFound) {
		t.Errorf("missing from err = %v", err)
	}
}

func repeat(s string, n int) string {
	var out []byte
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func TestUpdateProduct(t *testing.T) {
	m := newMemStores()
	p := seedOpenProduct(t, m, "LEITE INTEGRAL", "LT", nil)
	svc := newTestService(m)

	brand := "CAMPANESA"
	unit := "duzia"
	v, err := svc.UpdateProduct(context.Background(), p.ID, domain.ProductPatch{
		Name:  strPtr("LEITE INTEGRAL 1L"),
		Brand: &brand,
		Unit:  &unit,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if v.Name != "LEITE INTEGRAL 1L" || v.Brand == nil || *v.Brand != "CAMPANESA" || v.Unit != "DZ" {
		t.Errorf("updated view = %+v, want normalized DZ unit", v)
	}

	// Brand may be cleared with an empty string.
	empty := ""
	if _, err := svc.UpdateProduct(context.Background(), p.ID, domain.ProductPatch{Brand: &empty}); err != nil {
		t.Fatalf("clear brand: %v", err)
	}
	got, _ := m.Products().GetByID(context.Background(), p.ID)
	if got.Brand != nil {
		t.Errorf("brand = %+v, want nil after clear", got.Brand)
	}

	if _, err := svc.UpdateProduct(context.Background(), p.ID, domain.ProductPatch{Unit: strPtr("LITERS")}); !errors.Is(err, ErrInvalidFilter) {
		t.Errorf("bad unit err = %v", err)
	}
	if _, err := svc.UpdateProduct(context.Background(), 404, domain.ProductPatch{Name: strPtr("X")}); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("missing product err = %v", err)
	}
}

func TestDeleteProductGuardAndPurge(t *testing.T) {
	m := newMemStores()
	p := seedOpenProduct(t, m, "ARROZ BRANCO", "KG", nil)
	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "X"}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedStoreAndLog(t, m, sm, p, "KG", mustDecimal("1"), mustDecimal("4.00"), base)
	svc := newTestService(m)

	// Refused while logs still reference the product.
	if _, err := svc.DeleteProduct(context.Background(), p.ID, false); !errors.Is(err, ErrInUse) {
		t.Errorf("guard err = %v, want ErrInUse", err)
	}
	if _, err := m.Products().GetByID(context.Background(), p.ID); err != nil {
		t.Errorf("guard deleted the product: %v", err)
	}

	// purge: logs and product fall together.
	res, err := svc.DeleteProduct(context.Background(), p.ID, true)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if res.LogsDeleted != 1 {
		t.Errorf("logs_deleted = %d, want 1", res.LogsDeleted)
	}
	if _, err := m.Products().GetByID(context.Background(), p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("product still present after purge")
	}

	if _, err := svc.DeleteProduct(context.Background(), 404, false); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("missing product err = %v", err)
	}
}

func TestDeleteSupermarketGuard(t *testing.T) {
	m := newMemStores()
	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO"}
	if err := m.Supermarkets().Upsert(context.Background(), sm); err != nil {
		t.Fatal(err)
	}
	nf := &domain.Nfce{AccessKey: "35" + repeat("7", 42), Supermarket: *sm}
	if err := m.Nfces().Create(context.Background(), nf); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(m)

	if err := svc.DeleteSupermarket(context.Background(), sm.ID); !errors.Is(err, ErrInUse) {
		t.Errorf("guard err = %v, want ErrInUse", err)
	}

	free := &domain.Supermarket{CNPJ: "12345678000378", LegalName: "VAZIO"}
	if err := m.Supermarkets().Upsert(context.Background(), free); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSupermarket(context.Background(), free.ID); err != nil {
		t.Fatalf("delete empty store: %v", err)
	}
	if _, err := m.Supermarkets().GetByCNPJ(context.Background(), free.CNPJ); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("store still present after delete")
	}

	if err := svc.DeleteSupermarket(context.Background(), 404); !errors.Is(err, ErrSupermarketNotFound) {
		t.Errorf("missing store err = %v", err)
	}
}

func TestListUsersAndUpdateRole(t *testing.T) {
	m := newMemStores()
	admin := &domain.User{Email: "admin@example.com", Role: "admin"}
	target := &domain.User{Email: "alice@example.com", Role: "user"}
	if err := m.Users().Create(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	if err := m.Users().Create(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(m)

	page, err := svc.ListUsers(context.Background(), 1, 1)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(page.Items) != 1 {
		t.Errorf("items = %d, want 1 (page of 1)", len(page.Items))
	}
	if page.Page.Total != 2 {
		t.Errorf("total = %d, want 2", page.Page.Total)
	}

	if err := svc.UpdateUserRole(context.Background(), admin.ID, target.ID, "admin"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	got, _ := m.Users().GetByID(context.Background(), target.ID)
	if got.Role != "admin" {
		t.Errorf("role = %q, want admin", got.Role)
	}

	if err := svc.UpdateUserRole(context.Background(), admin.ID, target.ID, "root"); !errors.Is(err, ErrInvalidRole) {
		t.Errorf("bad role err = %v", err)
	}
	if err := svc.UpdateUserRole(context.Background(), admin.ID, admin.ID, "user"); !errors.Is(err, ErrForbidden) {
		t.Errorf("self-change err = %v, want ErrForbidden", err)
	}
	if err := svc.UpdateUserRole(context.Background(), admin.ID, 404, "user"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("missing user err = %v", err)
	}
}

func TestDeleteReceipt(t *testing.T) {
	m := newMemStores()
	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO"}
	if err := m.Supermarkets().Upsert(context.Background(), sm); err != nil {
		t.Fatal(err)
	}
	owner := int64(1)
	nf := &domain.Nfce{AccessKey: "35" + repeat("2", 42), Supermarket: *sm, CreatedBy: &owner}
	if err := m.Nfces().Create(context.Background(), nf); err != nil {
		t.Fatal(err)
	}
	l := &domain.PriceLog{NfceID: nf.ID, Line: 1, Unit: "UN", Qty: mustDecimal("1"), TotalPrice: mustDecimal("2.00")}
	if err := m.PriceLogs().Create(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	pv := &domain.Preview{UserID: 7, AccessKey: nf.AccessKey, Status: "pending", TTL: time.Now().Add(time.Hour)}
	if err := m.Previews().Create(context.Background(), pv); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(m)

	// A different, non-admin caller may not delete someone else's receipt.
	if _, err := svc.DeleteReceipt(context.Background(), 2, DeleteReceiptRequest{AccessKey: nf.AccessKey}, false); !errors.Is(err, ErrForbidden) {
		t.Errorf("foreign delete err = %v, want ErrForbidden", err)
	}

	res, err := svc.DeleteReceipt(context.Background(), owner, DeleteReceiptRequest{AccessKey: nf.AccessKey}, false)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if res.LogsDeleted != 1 || res.PreviewsDeleted != 1 {
		t.Errorf("result = %+v", res)
	}
	if _, err := m.Nfces().GetByAccessKey(context.Background(), nf.AccessKey); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("nfce still present after delete")
	}
	if got := len(m.nfceLogs(nf.ID)); got != 0 {
		t.Errorf("logs remaining = %d", got)
	}

	// An admin may delete any receipt.
	nf2 := &domain.Nfce{AccessKey: "35" + repeat("3", 42), Supermarket: *sm, CreatedBy: int64Ptr(9)}
	if err := m.Nfces().Create(context.Background(), nf2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteReceipt(context.Background(), 8, DeleteReceiptRequest{AccessKey: nf2.AccessKey}, true); err != nil {
		t.Fatalf("admin delete: %v", err)
	}

	if _, err := svc.DeleteReceipt(context.Background(), owner, DeleteReceiptRequest{AccessKey: "35" + repeat("9", 42)}, true); !errors.Is(err, ErrReceiptNotFound) {
		t.Errorf("missing receipt err = %v", err)
	}
	if _, err := svc.DeleteReceipt(context.Background(), owner, DeleteReceiptRequest{AccessKey: "  "}, true); !errors.Is(err, ErrInvalidFilter) {
		t.Errorf("blank key err = %v", err)
	}
}

func (m *memStores) nfceLogs(nfceID int64) []*domain.PriceLog {
	var out []*domain.PriceLog
	for _, l := range m.priceLogs {
		if l.NfceID == nfceID {
			out = append(out, l)
		}
	}
	return out
}
