package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"meupreco/internal/domain"
)

func TestSearchFiltered(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	rows := []*domain.Product{
		{ProductKey: "p1", GTIN: stringPtr("07896008038717"), Name: "ARROZ BRANCO 5KG", Brand: stringPtr("TIO JOAO"), Unit: "UN"},
		{ProductKey: "p2", Name: "LIMÃO TAHITI", Brand: stringPtr("CAMPANESA"), Unit: "UN", CanonicalStem: stringPtr("limao tahiti")},
		{ProductKey: "p3", Name: "LEITE INTEGRAL 1L", Brand: stringPtr("PARMALAT"), Unit: "UN"},
		{ProductKey: "px", Name: "ERRO DE OCR", Unit: "UN", NeedsReview: true},
	}
	for i, p := range rows {
		if _, err := s.Products().Create(ctx, p); err != nil {
			t.Fatalf("create product %d: %v", i, err)
		}
	}

	items, total, err := s.Products().SearchFiltered(ctx, domain.ProductFilter{}, 10, 0)
	if err != nil {
		t.Fatalf("search all: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 (review row hidden)", total)
	}
	for _, p := range items {
		if p.NeedsReview {
			t.Fatalf("needs_review product leaked into public read: %+v", p)
		}
	}

	// Trigram query over the accent-stripped stem: "limao" finds LIMÃO.
	items, total, err = s.Products().SearchFiltered(ctx, domain.ProductFilter{Query: "limao"}, 10, 0)
	if err != nil {
		t.Fatalf("search query: %v", err)
	}
	if len(items) != 1 || items[0].Name != "LIMÃO TAHITI" || total != 1 {
		t.Errorf("query hits = %+v (total %d)", items, total)
	}

	// GTIN prefix matched against the stored zero-stripped 14-digit barcode.
	items, total, err = s.Products().SearchFiltered(ctx, domain.ProductFilter{GTIN: "7896008"}, 10, 0)
	if err != nil {
		t.Fatalf("search gtin: %v", err)
	}
	if len(items) != 1 || total != 1 {
		t.Errorf("gtin hits = %+v (total %d)", items, total)
	}

	// Exact canonical unit and brand-substring filter independently.
	items, total, err = s.Products().SearchFiltered(ctx, domain.ProductFilter{Unit: "UN", Brand: "PARMA"}, 10, 0)
	if err != nil {
		t.Fatalf("search unit+brand: %v", err)
	}
	if len(items) != 1 || items[0].Name != "LEITE INTEGRAL 1L" || total != 1 {
		t.Errorf("combo hits = %+v (total %d)", items, total)
	}

	// Pagination returns the same total as the unfiltered count.
	page2, total2, err := s.Products().SearchFiltered(ctx, domain.ProductFilter{}, 2, 2)
	if err != nil {
		t.Fatalf("search page: %v", err)
	}
	if len(page2) != 1 || total2 != 3 {
		t.Errorf("page2 = %+v (total %d)", page2, total2)
	}
}

func TestProductUpdate(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	p := &domain.Product{ProductKey: "p1", GTIN: stringPtr("07896008038717"),
		Name: "ARROZ", Brand: stringPtr("TIO JOAO"), Unit: "UN"}
	if _, err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	name := "ARROZ BRANCO 5KG RENOMEADO"
	unit := "KG"
	if err := s.Products().Update(ctx, p.ID, domain.ProductPatch{Name: &name, Unit: &unit}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.Products().GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != name || got.Unit != "KG" || got.Brand == nil || *got.Brand != "TIO JOAO" {
		t.Errorf("update not applied: %+v", got)
	}

	// Empty-brand pointer clears the column.
	clear := ""
	if err := s.Products().Update(ctx, p.ID, domain.ProductPatch{Brand: &clear}); err != nil {
		t.Fatalf("clear brand: %v", err)
	}
	got, err = s.Products().GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got.Brand != nil {
		t.Errorf("brand = %v, want nil", *got.Brand)
	}

	if err := s.Products().Update(ctx, 999999, domain.ProductPatch{Name: &name}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update missing = %v, want ErrNotFound", err)
	}
}

func TestProductDeleteGuard(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	sm := seedSupermarket(t, s)
	n := seedNfce(t, s, sm)
	p := &domain.Product{ProductKey: "p1", Name: "ARROZ", Unit: "UN"}

	if _, err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	pid := p.ID
	if err := s.PriceLogs().Create(ctx, &domain.PriceLog{
		NfceID: n.ID, ProductID: &pid, Line: 1, DescriptionSnapshot: "ARROZ",
		Qty: decimal.NewFromFloat(1), Unit: "UN", UnitPrice: decimal.NewFromFloat(10),
		TotalPrice: decimal.NewFromFloat(10), IdentityStatus: "gtin_known",
	}); err != nil {
		t.Fatalf("create log: %v", err)
	}

	// FK still referencing: the delete aborts (the service purges first).
	if err := s.Products().Delete(ctx, p.ID); err == nil {
		t.Fatal("deleted a product still referenced by price_logs; want FK error")
	}

	if _, err := s.PriceLogs().DeleteByProduct(ctx, p.ID); err != nil {
		t.Fatalf("purge logs: %v", err)
	}
	if err := s.Products().Delete(ctx, p.ID); err != nil {
		t.Fatalf("delete after purge: %v", err)
	}
	if err := s.Products().Delete(ctx, p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("double delete = %v, want ErrNotFound", err)
	}
}

func TestSupermarketListRePointDelete(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	a := &domain.Supermarket{CNPJ: "11111111000111", LegalName: "MERCADO A", Address: "Rua A"}
	b := &domain.Supermarket{CNPJ: "22222222000122", LegalName: "MERCADO B", Address: "Rua B"}
	for _, sm := range []*domain.Supermarket{a, b} {
		if err := s.Supermarkets().Upsert(ctx, sm); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	list, total, err := s.Supermarkets().List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("list = %+v (total %d), want 2", list, total)
	}
	if list[0].LegalName != "MERCADO A" || list[1].LegalName != "MERCADO B" {
		t.Errorf("order = %q, %q; want name-sorted A, B", list[0].LegalName, list[1].LegalName)
	}

	issued := time.Now().UTC()
	n := &domain.Nfce{AccessKey: "35241152323688000144650020000002571115675901",
		Supermarket: *a, IssuedAt: &issued, PaymentMethod: "Dinheiro",
		Total: decimal.NewFromFloat(1), Discount: decimal.NewFromFloat(0),
		PaidValue: decimal.NewFromFloat(1)}
	if err := s.Nfces().Create(ctx, n); err != nil {
		t.Fatalf("create nfce: %v", err)
	}
	if got, err := s.Supermarkets().CountNfces(ctx, a.ID); err != nil || got != 1 {
		t.Fatalf("count a = %d, %v; want 1", got, err)
	}
	if got, err := s.Supermarkets().CountNfces(ctx, b.ID); err != nil || got != 0 {
		t.Fatalf("count b = %d, %v; want 0", got, err)
	}

	moved, err := s.Supermarkets().RePointNfces(ctx, a.ID, b.ID)
	if err != nil || moved != 1 {
		t.Fatalf("repoint = %d, %v; want 1", moved, err)
	}
	if got, err := s.Supermarkets().CountNfces(ctx, a.ID); err != nil || got != 0 {
		t.Fatalf("count a after = %d, %v; want 0", got, err)
	}
	byID, err := s.Nfces().GetByID(ctx, n.ID)
	if err != nil {
		t.Fatalf("get nfce: %v", err)
	}
	if byID.Supermarket.ID != b.ID {
		t.Errorf("nfce now on %d, want %d", byID.Supermarket.ID, b.ID)
	}

	// from == to is a cheap no-op.
	moved, err = s.Supermarkets().RePointNfces(ctx, b.ID, b.ID)
	if err != nil || moved != 0 {
		t.Fatalf("self repoint = %d, %v; want 0", moved, err)
	}
	if got, err := s.Supermarkets().CountNfces(ctx, b.ID); err != nil || got != 1 {
		t.Fatalf("count b after = %d, %v; want 1", got, err)
	}

	// A store still owning receipts cannot be deleted (FK); the now-empty one
	// vanishes.
	if err := s.Supermarkets().Delete(ctx, b.ID); err == nil {
		t.Fatal("deleted store still owning receipts; want FK error")
	}
	if err := s.Supermarkets().Delete(ctx, a.ID); err != nil {
		t.Fatalf("delete empty store: %v", err)
	}
	if err := s.Supermarkets().Delete(ctx, a.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("double delete = %v, want ErrNotFound", err)
	}
}

func TestUserListUpdateRole(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	// Distinct emails: unique constraint. Created in a fixed loop so the
	// newest-first order is deterministic for the assertion.
	for i, email := range []string{"one@example.com", "two@example.com", "three@example.com"} {
		u := &domain.User{Name: email, Email: email, PasswordHash: "h", Role: "user"}
		if err := s.Users().Create(ctx, u); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	list, total, err := s.Users().List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// The shared test DB may also carry the boot-time admin seeded by the
	// cmd/server suite, so assert on our three rows and their relative order
	// rather than an exact global total.
	if total < 3 || len(list) < 3 {
		t.Fatalf("list = %+v (total %d), want at least our 3 users", list, total)
	}
	positions := map[string]int{}
	for i, u := range list {
		if u.Email == "one@example.com" || u.Email == "two@example.com" || u.Email == "three@example.com" {
			positions[u.Email] = i
		}
	}
	if len(positions) != 3 || positions["three@example.com"] >= positions["two@example.com"] ||
		positions["two@example.com"] >= positions["one@example.com"] {
		t.Errorf("newest-first order wrong: %+v (list %+v)", positions, list)
	}

	byEmail, err := s.Users().GetByEmail(ctx, "two@example.com")
	if err != nil {
		t.Fatalf("get by email: %v", err)
	}
	if err := s.Users().UpdateRole(ctx, byEmail.ID, "admin"); err != nil {
		t.Fatalf("update role: %v", err)
	}
	got, err := s.Users().GetByID(ctx, byEmail.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Role != "admin" {
		t.Errorf("role = %q, want admin", got.Role)
	}
	if err := s.Users().UpdateRole(ctx, 999999, "user"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("update missing = %v, want ErrNotFound", err)
	}
}

func TestNfceDelete(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	sm := seedSupermarket(t, s)
	n := seedNfce(t, s, sm)
	if err := s.Nfces().Delete(ctx, n.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Nfces().Delete(ctx, n.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("double delete = %v, want ErrNotFound", err)
	}
}

func TestPreviewDeleteByAccessKey(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	u1 := &domain.User{Name: "P1", Email: "p1@example.com", PasswordHash: "h", Role: "user"}
	u2 := &domain.User{Name: "P2", Email: "p2@example.com", PasswordHash: "h", Role: "user"}
	if err := s.Users().Create(ctx, u1); err != nil {
		t.Fatalf("create u1: %v", err)
	}
	if err := s.Users().Create(ctx, u2); err != nil {
		t.Fatalf("create u2: %v", err)
	}

	key := "35241152323688000144650020000002571115675901"
	for _, u := range []*domain.User{u1, u2} {
		if err := s.Previews().Create(ctx, &domain.Preview{
			UserID: u.ID, AccessKey: key, PlanItems: []byte("[]"),
			PlanTotals: []byte("{}"), Status: "pending",
			TTL: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("create preview: %v", err)
		}
	}

	n, err := s.Previews().DeleteByAccessKey(ctx, key)
	if err != nil {
		t.Fatalf("delete by access key: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d, want 2 (both users' drafts)", n)
	}
	if n, err := s.Previews().DeleteByAccessKey(ctx, key); err != nil || n != 0 {
		t.Errorf("second delete = %d, %v; want 0", n, err)
	}
}

// nfceSeq hands out unique access keys across the suite.
var nfceSeq int64

func seedPriceLog(t *testing.T, s *Store, sm *domain.Supermarket, p *domain.Product, issued time.Time, total string) {
	t.Helper()
	ctx := context.Background()
	nfceSeq++
	n := &domain.Nfce{
		AccessKey:   fmt.Sprintf("35241152323688000144650020000002571115675%06d", nfceSeq),
		Supermarket: *sm, IssuedAt: &issued, PaymentMethod: "Dinheiro",
		Total: decimal.NewFromFloat(1), Discount: decimal.NewFromFloat(0),
		PaidValue: decimal.NewFromFloat(1),
	}
	if err := s.Nfces().Create(ctx, n); err != nil {
		t.Fatalf("create nfce: %v", err)
	}
	l := &domain.PriceLog{
		NfceID: n.ID, Line: 1, DescriptionSnapshot: p.Name,
		Qty: decimal.NewFromFloat(1), Unit: p.Unit,
		UnitPrice: decimal.RequireFromString(total), TotalPrice: decimal.RequireFromString(total),
		IdentityStatus: "gtin_known",
	}
	if p.ID != 0 {
		l.ProductID = &p.ID
	}
	if err := s.PriceLogs().Create(ctx, l); err != nil {
		t.Fatalf("create log: %v", err)
	}
}

func TestPriceLogHistory(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	sm := seedSupermarket(t, s)
	p := &domain.Product{ProductKey: "p1", Name: "ARROZ BRANCO 5KG", Unit: "UN"}
	if _, err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("create product: %v", err)
	}

	base := time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)
	seedPriceLog(t, s, sm, p, base, "20.00")
	seedPriceLog(t, s, sm, p, base.Add(7*24*time.Hour), "22.00")
	seedPriceLog(t, s, sm, p, base.Add(14*24*time.Hour), "24.00")

	pts, err := s.PriceLogs().HistoryByProduct(ctx, p.ID, nil, nil)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(pts) != 3 {
		t.Fatalf("points = %d, want 3", len(pts))
	}
	// Issued-at ascending, annotated with the emitter.
	if !pts[0].IssuedAt.Equal(base) || pts[2].StoreName != sm.LegalName ||
		pointsOrderedAsc(pts) == false {
		t.Errorf("order/annotation wrong: %+v", pts)
	}
	if !pts[2].TotalPrice.Equal(decimal.RequireFromString("24.00")) {
		t.Errorf("last price = %v, want 24.00", pts[2].TotalPrice)
	}

	// Window trims both ends (inclusive bounds).
	from := base.Add(7 * 24 * time.Hour)
	to := base.Add(7 * 24 * time.Hour)
	windowed, err := s.PriceLogs().HistoryByProduct(ctx, p.ID, &from, &to)
	if err != nil {
		t.Fatalf("windowed history: %v", err)
	}
	if len(windowed) != 1 || !pointsOrderedAsc(windowed) {
		t.Errorf("windowed = %+v, want exactly the middle point", windowed)
	}
}

func pointsOrderedAsc(pts []domain.PricePoint) bool {
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1].IssuedAt, pts[i].IssuedAt
		if a == nil || b == nil || !a.Before(*b) {
			return false
		}
	}
	return true
}

func TestPriceLogLatestBySupermarket(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	sm := seedSupermarket(t, s)
	// Product with a review row is invisible even when it has logs.
	hidden := &domain.Product{ProductKey: "hidden", Name: "SEM PRECO CONFIADO", Unit: "UN", NeedsReview: true}
	if _, err := s.Products().Create(ctx, hidden); err != nil {
		t.Fatalf("create hidden: %v", err)
	}
	p := &domain.Product{ProductKey: "p1", Name: "ARROZ BRANCO 5KG", Unit: "UN"}
	if _, err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("create product: %v", err)
	}

	base := time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)
	seedPriceLog(t, s, sm, p, base, "20.00")
	seedPriceLog(t, s, sm, p, base.Add(24*time.Hour), "22.00")
	seedPriceLog(t, s, sm, hidden, base, "1.00")

	rows, total, err := s.PriceLogs().LatestBySupermarket(ctx, sm.ID, 10, 0)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("rows = %+v (total %d), want only p1", rows, total)
	}
	row := rows[0]
	if row.Product.Name != "ARROZ BRANCO 5KG" || !row.Qty.Equal(decimal.NewFromFloat(1)) {
		t.Errorf("row = %+v", row)
	}
	// Most recent log wins (DISTINCT ON ordered by issued_at DESC).
	if !row.TotalPrice.Equal(decimal.RequireFromString("22.00")) {
		t.Errorf("latest price = %v, want 22.00", row.TotalPrice)
	}
	if row.PriceLogID == 0 || row.LastIssuedAt == nil {
		t.Errorf("latest annotation missing: %+v", row)
	}
}

func TestPriceLogNeedsReview(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"}
	if err := s.Supermarkets().Upsert(ctx, sm); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	n := seedNfce(t, s, sm)
	code := "10293"
	review := &domain.PriceLog{
		NfceID: n.ID, Line: 1, DescriptionSnapshot: "FRUTA AVULSA",
		StoreCode: code, Qty: decimal.NewFromFloat(1), Unit: "KG",
		UnitPrice: decimal.NewFromFloat(5.00), TotalPrice: decimal.NewFromFloat(5.00),
		IdentityStatus: "uid_uncertain", NeedsReview: true,
	}
	if err := s.PriceLogs().Create(ctx, review); err != nil {
		t.Fatalf("create review log: %v", err)
	}

	items, total, err := s.PriceLogs().NeedsReview(ctx, 10, 0)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("queue = %+v (total %d), want 1", items, total)
	}
	it := items[0]
	if it.PriceLogID != review.ID || it.Description != "FRUTA AVULSA" ||
		it.StoreCode != code || it.CNPJBase != "12345678" ||
		it.StoreName != "MERCADO BOM PRECO LTDA" {
		t.Errorf("queue item annotations wrong: %+v", it)
	}
	if !it.TotalPrice.Equal(decimal.NewFromFloat(5.00)) || it.Qty.Equal(decimal.Zero) {
		t.Errorf("queue item money wrong: %+v", it)
	}
}

func TestPriceLogSetProduct(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	admin := seedUser(t, s)
	sm := seedSupermarket(t, s)
	n := seedNfce(t, s, sm)
	l := &domain.PriceLog{
		NfceID: n.ID, Line: 1, DescriptionSnapshot: "FRUTA AVULSA",
		StoreCode: "10293", Qty: decimal.NewFromFloat(1), Unit: "KG",
		UnitPrice: decimal.NewFromFloat(5.00), TotalPrice: decimal.NewFromFloat(5.00),
		IdentityStatus: "uid_uncertain", NeedsReview: true,
	}
	if err := s.PriceLogs().Create(ctx, l); err != nil {
		t.Fatalf("create review log: %v", err)
	}
	p := &domain.Product{ProductKey: "cm-maca", Name: "Maçã Fuji", Unit: "KG",
		CanonicalStem: stringPtr("maca fuji")}
	if _, err := s.Products().Create(ctx, p); err != nil {
		t.Fatalf("create product: %v", err)
	}

	if err := s.PriceLogs().SetProduct(ctx, l.ID, p.ID, admin.ID, "match by stem"); err != nil {
		t.Fatalf("set product: %v", err)
	}
	got, err := s.PriceLogs().GetByID(ctx, l.ID)
	if err != nil {
		t.Fatalf("get log: %v", err)
	}
	if got.ProductID == nil || *got.ProductID != p.ID ||
		got.NeedsReview || got.ResolvedBy == nil || *got.ResolvedBy != admin.ID ||
		got.ResolutionNote == nil || *got.ResolutionNote != "match by stem" {
		t.Errorf("log after assign = %+v", got)
	}

	// A stale log id is a hard-not-found for the batch tx.
	if err := s.PriceLogs().SetProduct(ctx, 999999, p.ID, admin.ID, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("set missing = %v, want ErrNotFound", err)
	}
}

func TestPriceLogRePointAndDelete(t *testing.T) {
	s := testStore
	truncate(t)
	ctx := context.Background()

	sm := seedSupermarket(t, s)
	n := seedNfce(t, s, sm)
	from := &domain.Product{ProductKey: "from", Name: "PRODUTO ANTIGO", Unit: "UN"}
	to := &domain.Product{ProductKey: "to", Name: "PRODUTO NOVO", Unit: "UN"}
	for _, p := range []*domain.Product{from, to} {
		if _, err := s.Products().Create(ctx, p); err != nil {
			t.Fatalf("create product: %v", err)
		}
	}
	for line := 1; line <= 3; line++ {
		l := &domain.PriceLog{
			NfceID: n.ID, Line: line, DescriptionSnapshot: "PRODUTO ANTIGO",
			ProductID: &from.ID, Qty: decimal.NewFromFloat(1), Unit: "UN",
			UnitPrice: decimal.NewFromFloat(10), TotalPrice: decimal.NewFromFloat(10),
			IdentityStatus: "gtin_known",
		}
		if err := s.PriceLogs().Create(ctx, l); err != nil {
			t.Fatalf("create log %d: %v", line, err)
		}
	}

	moved, err := s.PriceLogs().RePointProduct(ctx, from.ID, to.ID)
	if err != nil || moved != 3 {
		t.Fatalf("repoint = %d, %v; want 3", moved, err)
	}
	if n, err := s.PriceLogs().CountByProduct(ctx, from.ID); err != nil || n != 0 {
		t.Fatalf("count from = %d, %v; want 0", n, err)
	}
	if n, err := s.PriceLogs().CountByProduct(ctx, to.ID); err != nil || n != 3 {
		t.Fatalf("count to = %d, %v; want 3", n, err)
	}

	removed, err := s.PriceLogs().DeleteByNfceID(ctx, n.ID)
	if err != nil || removed != 3 {
		t.Fatalf("delete by nfce = %d, %v; want 3", removed, err)
	}
	if _, err := s.PriceLogs().GetByID(ctx, to.ID); err == nil {
		t.Fatal("log still present after delete-by-nfce")
	}
}
