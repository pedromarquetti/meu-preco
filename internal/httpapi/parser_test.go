package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"meupreco/internal/auth"
	"meupreco/internal/config"
	"meupreco/internal/domain"
	pstore "meupreco/internal/store/postgres"
)

// handler tests run against the shared MEUPRECO_TEST_DATABASE_URL and
// reuse the schema-advisory-locked harness from nfce_ingest_test.go. The router
// under test is the real one; only the bearer-token role varies.

// httpNfceSeq hands out unique receipt keys across the suite.
var httpNfceSeq atomic.Int64

// roleRouter is authRouter with a caller-chosen role claim.
func roleRouter(t *testing.T, stores domain.Stores, res nfceResolver, userID int64, role string) http.Handler {
	t.Helper()
	cfg := config.Config{
		CORSOrigins:   []string{"https://app.example.com"},
		JWTSecret:     testJWTSecret,
		JWTAccessTTL:  30 * time.Minute,
		JWTRefreshTTL: 24 * time.Hour,
	}
	tokens := auth.NewTokenService(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	u := &domain.User{ID: userID, Email: "user@example.com", Role: role}
	token, err := tokens.IssueAccess(u)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	handler := newRouter(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), res, stores, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		handler.ServeHTTP(w, r)
	})
}

func do(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func jint(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return 0
}

// seedHTTPLog adds a supermarket with a created_by owner plus one nfce with
// a price_log (optionally flagged for review) — every row needed by the admin
// flow in a single call.
func seedHTTPLog(t *testing.T, st *pstore.Store, sm *domain.Supermarket, unit string, p *domain.Product, issued time.Time, price string, review bool, createdBy int64) *domain.PriceLog {
	t.Helper()
	ctx := context.Background()
	n := &domain.Nfce{
		AccessKey:   fmt.Sprintf("35241152323688000144650020000002571115675%08d", httpNfceSeq.Add(1)),
		Supermarket: *sm, IssuedAt: &issued, PaymentMethod: "Dinheiro",
		Total: decimal.NewFromFloat(1), Discount: decimal.NewFromFloat(0),
		PaidValue: decimal.NewFromFloat(1), CreatedBy: &createdBy,
	}
	if err := st.Nfces().Create(ctx, n); err != nil {
		t.Fatalf("seed nfce: %v", err)
	}
	ppu := decimal.RequireFromString(price)
	l := &domain.PriceLog{
		NfceID: n.ID, Line: 1, DescriptionSnapshot: "FRUTA AVULSA",
		StoreCode: "77", Qty: decimal.NewFromFloat(1), Unit: unit,
		PricePerUnit:   &ppu,
		UnitPrice:      decimal.RequireFromString(price),
		TotalPrice:     decimal.RequireFromString(price),
		IdentityStatus: "uid_uncertain", NeedsReview: review,
	}
	if !review && p.ID != 0 {
		l.ProductID = &p.ID
	}
	if err := st.PriceLogs().Create(ctx, l); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	return l
}

func TestSharedReads(t *testing.T) {
	st := integrationStore(t)
	if st == nil {
		return
	}
	truncatePG(t, st.Pool())
	ctx := context.Background()

	user := &domain.User{Email: "reader@example.com", PasswordHash: "h", Role: "user"}
	if err := st.Users().Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	router := roleRouter(t, st, pgResolver{}, user.ID, "user")

	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"}
	if err := st.Supermarkets().Upsert(ctx, sm); err != nil {
		t.Fatalf("upsert supermarket: %v", err)
	}
	pub := &domain.Product{ProductKey: "07891000412855", GTIN: strptr("07891000412855"),
		Name: "ACHOCOLATADO EM PO NESCAU 350G", Unit: "UN"}
	if err := st.Products().Create(ctx, pub); err != nil {
		t.Fatalf("create public product: %v", err)
	}
	hid := &domain.Product{ProductKey: "hid", Name: "ERRO DE OCR", Unit: "UN", NeedsReview: true}
	if err := st.Products().Create(ctx, hid); err != nil {
		t.Fatalf("create hidden product: %v", err)
	}

	base := time.Date(2024, 11, 1, 8, 0, 0, 0, time.UTC)
	seedHTTPLog(t, st, sm, "UN", pub, base, "20.00", false, user.ID)
	seedHTTPLog(t, st, sm, "UN", pub, base.Add(7*24*time.Hour), "22.00", false, user.ID)
	// The review product's log must never surface either: its product is
	// needs_review, so LatestBySupermarket filters it out.
	seedHTTPLog(t, st, sm, "UN", &domain.Product{ProductKey: "hid-log", Name: "ERRO DE OCR", Unit: "UN"}, base, "5.00", true, user.ID)

	// Search: needs_review product is invisible.
	rec, out := do(t, router, http.MethodGet, "/api/v1/products?limit=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d (%s)", rec.Code, rec.Body.String())
	}
	items := out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("search items = %d, want only the public product (%s)", len(items), rec.Body.String())
	}
	if it := items[0].(map[string]any); it["name"] != "ACHOCOLATADO EM PO NESCAU 350G" {
		t.Errorf("search hit = %v", it["name"])
	}
	if pg := out["page"].(map[string]any); jint(pg["total"]) != 1 {
		t.Errorf("search total = %v, want 1", pg["total"])
	}

	// GTIN prefix and trigram query both hit.
	if rec, _ := do(t, router, http.MethodGet, "/api/v1/products?gtin=7891000412", ""); rec.Code != http.StatusOK || len(rebodyItems(t, rec)) != 1 {
		t.Errorf("gtin filter status = %d", rec.Code)
	}
	if rec, _ := do(t, router, http.MethodGet, "/api/v1/products?q=nescau", ""); rec.Code != http.StatusOK || len(rebodyItems(t, rec)) != 1 {
		t.Errorf("q filter status = %d", rec.Code)
	}

	// Invalid filter: a bad unit token, an oversized limit and malformed /
	// non-positive page values are 400 invalid_filter (page must be rejected,
	// not silently rebuilt into page 1).
	for _, q := range []string{"unit=d%C3%BAzia", "limit=999", "page=abc", "page=0", "page=-1"} {
		rec, out = do(t, router, http.MethodGet, "/api/v1/products?"+q, "")
		if rec.Code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_filter" {
			t.Errorf("filter %q = %d (%s)", q, rec.Code, rec.Body.String())
		}
	}
	// A well-formed page is accepted; the request is honored, not rewritten.
	if rec, out = do(t, router, http.MethodGet, "/api/v1/products?page=2", ""); rec.Code != http.StatusOK {
		t.Errorf("page=2 = %d (%s)", rec.Code, rec.Body.String())
	} else if jint(out["page"].(map[string]any)["page"]) != 2 {
		t.Errorf("page echo = %v, want 2", out["page"])
	}

	// One product by id; a missing one is a 404 not_found.
	rec, out = do(t, router, http.MethodGet, fmt.Sprintf("/api/v1/products/%d", pub.ID), "")
	if rec.Code != http.StatusOK || out["name"] != "ACHOCOLATADO EM PO NESCAU 350G" {
		t.Errorf("get = %d (%s)", rec.Code, rec.Body.String())
	}
	rec, out = do(t, router, http.MethodGet, "/api/v1/products/999999", "")
	if rec.Code != http.StatusNotFound || out["error"].(map[string]any)["code"] != "not_found" {
		t.Errorf("get missing = %d (%s)", rec.Code, rec.Body.String())
	}

	// History: points ordered by issued_at with the folded comparable price and
	// a per-comparable-unit aggregate.
	rec, out = do(t, router, http.MethodGet, fmt.Sprintf("/api/v1/products/%d/history", pub.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history status = %d (%s)", rec.Code, rec.Body.String())
	}
	pts := out["points"].([]any)
	if len(pts) != 2 {
		t.Fatalf("history points = %d, want 2", len(pts))
	}
	first := pts[0].(map[string]any)
	if first["comparable_unit"] != "UN" || first["comparable_price_per_unit"] != "20" {
		t.Errorf("first point = %+v", first)
	}
	agg := out["aggregate"].([]any)
	if len(agg) != 1 {
		t.Fatalf("aggregate = %d series, want 1", len(agg))
	}
	un := agg[0].(map[string]any)
	if un["unit"] != "UN" || jint(un["count"]) != 2 || un["min"] != "20" || un["max"] != "22" || un["last"] != "22" || un["avg"] != "21" {
		t.Errorf("UN aggregate = %+v", un)
	}

	// Window narrows to the first point; an inverted window is rejected.
	rec, out = do(t, router, http.MethodGet,
		fmt.Sprintf("/api/v1/products/%d/history?from=2024-11-01T00:00:00Z&to=2024-11-01T23:59:59Z", pub.ID), "")
	if rec.Code != http.StatusOK || len(out["points"].([]any)) != 1 {
		t.Errorf("windowed history = %d (%s)", rec.Code, rec.Body.String())
	}
	rec, out = do(t, router, http.MethodGet,
		fmt.Sprintf("/api/v1/products/%d/history?from=2024-12-31&to=2024-01-01", pub.ID), "")
	if rec.Code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_filter" {
		t.Errorf("inverted window = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec, _ := do(t, router, http.MethodGet, "/api/v1/products/999999/history", ""); rec.Code != http.StatusNotFound {
		t.Errorf("history missing product = %d", rec.Code)
	}

	// Supermarket reads: the list and the per-store catalog (review rows out).
	rec, out = do(t, router, http.MethodGet, "/api/v1/supermarkets?limit=50", "")
	if rec.Code != http.StatusOK || len(out["items"].([]any)) != 1 {
		t.Fatalf("supermarkets = %d (%s)", rec.Code, rec.Body.String())
	}
	rec, out = do(t, router, http.MethodGet, fmt.Sprintf("/api/v1/supermarkets/%d/products", sm.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("supermarket products = %d (%s)", rec.Code, rec.Body.String())
	}
	spItems := out["items"].([]any)
	if len(spItems) != 1 {
		t.Fatalf("store catalog = %d items, want 1 (%s)", len(spItems), rec.Body.String())
	}
	sp := spItems[0].(map[string]any)
	if sp["price_log_id"] == nil || sp["product"].(map[string]any)["name"] != "ACHOCOLATADO EM PO NESCAU 350G" {
		t.Errorf("store catalog row = %+v", sp)
	}
	// The review seed also logged the hidden product; the needs_review product
	// must not appear, so total == 1 stands even though the store filtered.
	if pg := out["page"].(map[string]any); jint(pg["total"]) != 1 {
		t.Errorf("store catalog total = %v, want 1", pg["total"])
	}

	// Unauthenticated reads are 401.
	raw := newRouter(config.Config{JWTSecret: testJWTSecret, CORSOrigins: []string{"https://app.example.com"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)), pgResolver{}, st, nil)
	rec, _ = do(t, raw, http.MethodGet, "/api/v1/products", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated read = %d, want 401", rec.Code)
	}
}

func rebodyItems(t *testing.T, rec *httptest.ResponseRecorder) []any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out["items"].([]any)
}

func TestRoleGating(t *testing.T) {
	st := integrationStore(t)
	if st == nil {
		return
	}
	truncatePG(t, st.Pool())
	ctx := context.Background()

	user := &domain.User{Email: "plain@example.com", PasswordHash: "h", Role: "user"}
	if err := st.Users().Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	unpriv := roleRouter(t, st, pgResolver{}, user.ID, "user")

	for _, path := range []string{
		"/api/v1/admin/users",
		"/api/v1/admin/products/needs-review",
		"/api/v1/supermarkets/1",
	} {
		method := http.MethodGet
		if path == "/api/v1/supermarkets/1" {
			method = http.MethodDelete
		}
		rec, out := do(t, unpriv, method, path, "")
		if rec.Code != http.StatusForbidden || out["error"].(map[string]any)["code"] != "forbidden" {
			t.Errorf("%s %s = %d (%s), want 403 forbidden", method, path, rec.Code, rec.Body.String())
		}
	}

	rec, _ := do(t, unpriv, http.MethodPost, "/api/v1/admin/products/batch-assign",
		`{"assignments":[{"id":1,"action":"existing","product_id":1}]}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("batch-assign as user = %d (%s), want 403", rec.Code, rec.Body.String())
	}

	// No token at all: RequireAuth answers 401 before the role middleware runs.
	raw := newRouter(config.Config{JWTSecret: testJWTSecret, CORSOrigins: []string{"https://app.example.com"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)), pgResolver{}, st, nil)
	rec, _ = do(t, raw, http.MethodGet, "/api/v1/admin/users", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated admin = %d, want 401", rec.Code)
	}
}

func TestAdminCuration(t *testing.T) {
	st := integrationStore(t)
	if st == nil {
		return
	}
	truncatePG(t, st.Pool())
	ctx := context.Background()

	admin := &domain.User{Email: "root@example.com", PasswordHash: "h", Role: "admin"}
	other := &domain.User{Email: "other@example.com", PasswordHash: "h", Role: "user"}
	if err := st.Users().Create(ctx, admin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := st.Users().Create(ctx, other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	api := roleRouter(t, st, pgResolver{}, admin.ID, "admin")

	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"}
	if err := st.Supermarkets().Upsert(ctx, sm); err != nil {
		t.Fatalf("upsert sm: %v", err)
	}
	pcur := &domain.Product{ProductKey: "prod-arroz", Name: "ARROZ BRANCO TIPO 1", Unit: "UN"}
	if err := st.Products().Create(ctx, pcur); err != nil {
		t.Fatalf("create product: %v", err)
	}
	base := time.Date(2024, 11, 1, 8, 0, 0, 0, time.UTC)
	reviewLog := seedHTTPLog(t, st, sm, "KG", pcur, base, "5.00", true, admin.ID)

	// mdup is the duplicate product a merge folds into pcur.
	mdup := &domain.Product{ProductKey: "prod-arroz-dupe", Name: "ARROZ BRANCO 5KG", Unit: "UN"}
	if err := st.Products().Create(ctx, mdup); err != nil {
		t.Fatalf("create mdup: %v", err)
	}
	seedHTTPLog(t, st, sm, "UN", mdup, base.Add(time.Hour), "6.00", false, admin.ID)

	// A second emitter to merge away, and an empty one to delete cleanly.
	sm2 := &domain.Supermarket{CNPJ: "22222222000122", LegalName: "MERCADO TAMBEM LTDA"}
	if err := st.Supermarkets().Upsert(ctx, sm2); err != nil {
		t.Fatalf("upsert sm2: %v", err)
	}
	seedHTTPLog(t, st, sm2, "UN", pcur, base.Add(2*time.Hour), "7.00", false, admin.ID)
	smEmpty := &domain.Supermarket{CNPJ: "44444444000144", LegalName: "MERCADO VAZIO LTDA"}
	if err := st.Supermarkets().Upsert(ctx, smEmpty); err != nil {
		t.Fatalf("upsert smEmpty: %v", err)
	}

	// 1. The review queue carries the store context annotations.
	rec, out := do(t, api, http.MethodGet, "/api/v1/admin/products/needs-review?limit=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("needs-review = %d (%s)", rec.Code, rec.Body.String())
	}
	qitems := out["items"].([]any)
	if len(qitems) != 1 {
		t.Fatalf("queue = %d items, want 1 (%s)", len(qitems), rec.Body.String())
	}
	q := qitems[0].(map[string]any)
	if q["description"] != "FRUTA AVULSA" || q["store_code"] != "77" || q["cnpj_base"] != "12345678" {
		t.Errorf("queue annotations = %+v", q)
	}
	if _, ok := q["suggested_stem"]; !ok {
		t.Errorf("queue item missing suggested_stem: %+v", q)
	}

	// 2. Batch-assign with a fresh product empties the queue.
	rec, out = do(t, api, http.MethodPost, "/api/v1/admin/products/batch-assign",
		fmt.Sprintf(`{"assignments":[{"id":%d,"action":"create","name":"MAÇA FUJI","brand":"IMPORTADA","unit":"kg"}]}`, reviewLog.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("batch-assign = %d (%s)", rec.Code, rec.Body.String())
	}
	if jint(out["assigned"]) != 1 || jint(out["created"]) != 1 {
		t.Errorf("batch result = %+v", out)
	}
	rec, out = do(t, api, http.MethodGet, "/api/v1/admin/products/needs-review?limit=50", "")
	if rec.Code != http.StatusOK || jint(out["page"].(map[string]any)["total"]) != 0 {
		t.Errorf("queue after assign = %d (%s)", rec.Code, rec.Body.String())
	}

	// 3. A malformed batch (missing ids/actions) is a clean 400.
	rec, out = do(t, api, http.MethodPost, "/api/v1/admin/products/batch-assign",
		`{"assignments":[{"id":0,"action":"existing","product_id":1}]}`)
	if rec.Code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_assignment" {
		t.Errorf("bad batch = %d (%s)", rec.Code, rec.Body.String())
	}

	// 4. Curate a product: rename/normalize unit and clear brand.
	rec, out = do(t, api, http.MethodPatch, fmt.Sprintf("/api/v1/admin/products/%d", pcur.ID),
		`{"name":"ARROZ BRANCO 5KG RENOMEADO","brand":"","unit":"kg"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch product = %d (%s)", rec.Code, rec.Body.String())
	}
	if out["name"] != "ARROZ BRANCO 5KG RENOMEADO" || out["unit"] != "KG" {
		t.Errorf("patched product = %+v", out)
	}
	if _, set := out["brand"]; set {
		t.Errorf("brand should be cleared, got %v", out["brand"])
	}
	rec, out = do(t, api, http.MethodPatch, fmt.Sprintf("/api/v1/admin/products/%d", pcur.ID),
		`{"unit":"dúzia"}`)
	if rec.Code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_filter" {
		t.Errorf("bad unit = %d (%s)", rec.Code, rec.Body.String())
	}

	// 5. Merge the duplicate into the curated product: logs move, dup vanishes.
	rec, out = do(t, api, http.MethodPost, "/api/v1/admin/products/merge",
		fmt.Sprintf(`{"from_id":%d,"to_id":%d}`, mdup.ID, pcur.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("merge products = %d (%s)", rec.Code, rec.Body.String())
	}
	if jint(out["logs_moved"]) != 1 || out["dup_removed"] != true {
		t.Errorf("merge result = %+v", out)
	}
	rec, _ = do(t, api, http.MethodGet, fmt.Sprintf("/api/v1/products/%d", mdup.ID), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("merged-away product = %d, want 404", rec.Code)
	}

	// 6. Product delete: guarded while referenced, purge drops them together.
	rec, out = do(t, api, http.MethodDelete, fmt.Sprintf("/api/v1/admin/products/%d", pcur.ID), "")
	if rec.Code != http.StatusConflict || out["error"].(map[string]any)["code"] != "in_use" {
		t.Errorf("guarded delete = %d (%s)", rec.Code, rec.Body.String())
	}
	rec, out = do(t, api, http.MethodDelete, fmt.Sprintf("/api/v1/admin/products/%d?purge=true", pcur.ID), "")
	if rec.Code != http.StatusOK || jint(out["logs_deleted"]) != 2 {
		t.Errorf("purge = %d (%s)", rec.Code, rec.Body.String())
	}
	rec, _ = do(t, api, http.MethodGet, fmt.Sprintf("/api/v1/products/%d", pcur.ID), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("purged product = %d, want 404", rec.Code)
	}

	// 7. Supermarket merge by CNPJ.
	rec, out = do(t, api, http.MethodPost, "/api/v1/admin/supermarkets/merge",
		`{"from_cnpj":"22222222000122","to_cnpj":"12345678000199"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge supermarkets = %d (%s)", rec.Code, rec.Body.String())
	}
	if jint(out["nfces_moved"]) != 1 || out["dup_removed"] != true {
		t.Errorf("merget supermarkets = %+v", out)
	}

	// 8. Supermarket delete: refuses while it owns receipts, frees the empty one.
	if smOwns, err := st.Supermarkets().CountNfces(ctx, sm.ID); err != nil || smOwns == 0 {
		t.Fatalf("sm nfces = %d, %v", smOwns, err)
	}
	rec, out = do(t, api, http.MethodDelete, fmt.Sprintf("/api/v1/supermarkets/%d", sm.ID), "")
	if rec.Code != http.StatusConflict || out["error"].(map[string]any)["code"] != "in_use" {
		t.Errorf("guarded store delete = %d (%s)", rec.Code, rec.Body.String())
	}
	rec, _ = do(t, api, http.MethodDelete, fmt.Sprintf("/api/v1/supermarkets/%d", smEmpty.ID), "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("free store delete = %d, want 204", rec.Code)
	}
	rec, _ = do(t, api, http.MethodDelete, fmt.Sprintf("/api/v1/supermarkets/%d", smEmpty.ID), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("double store delete = %d, want 404", rec.Code)
	}

	// 9. Users list (no password leak), role changes with 403/400 guards.
	rec, out = do(t, api, http.MethodGet, "/api/v1/admin/users?limit=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("users list = %d (%s)", rec.Code, rec.Body.String())
	}
	var foundAdmin, foundOther bool
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if _, leak := m["password_hash"]; leak {
			t.Fatalf("users list leaked password_hash: %+v", m)
		}
		switch m["email"] {
		case "root@example.com":
			foundAdmin = m["role"] == "admin"
		case "other@example.com":
			foundOther = m["role"] == "user"
		}
	}
	if !foundAdmin || !foundOther {
		t.Errorf("users list = %+v", out["items"])
	}
	rec, _ = do(t, api, http.MethodPatch, fmt.Sprintf("/api/v1/admin/users/%d/role", other.ID),
		`{"role":"admin"}`)
	if rec.Code != http.StatusNoContent {
		t.Errorf("promote other = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	rec, out = do(t, api, http.MethodPatch, fmt.Sprintf("/api/v1/admin/users/%d/role", admin.ID),
		`{"role":"user"}`)
	if rec.Code != http.StatusForbidden || out["error"].(map[string]any)["code"] != "forbidden" {
		t.Errorf("self role change = %d (%s)", rec.Code, rec.Body.String())
	}
	rec, out = do(t, api, http.MethodPatch, fmt.Sprintf("/api/v1/admin/users/%d/role", other.ID),
		`{"role":"superuser"}`)
	if rec.Code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_role" {
		t.Errorf("bad role = %d (%s)", rec.Code, rec.Body.String())
	}

	// 10. Admin may delete a receipt it does not own.
	var targetNfceID int64
	var targetKey string
	if err := st.Pool().QueryRow(ctx,
		`SELECT id, access_key FROM nfces WHERE access_key != '' ORDER BY id DESC LIMIT 1`).
		Scan(&targetNfceID, &targetKey); err != nil {
		t.Fatalf("pick nfce: %v", err)
	}
	rec, out = do(t, api, http.MethodPost, "/api/v1/nfce/delete",
		`{"access_key":"`+targetKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin delete receipt = %d (%s)", rec.Code, rec.Body.String())
	}
	if jint(out["nfce_id"]) != targetNfceID {
		t.Errorf("deleted nfce_id = %v, want %d", out["nfce_id"], targetNfceID)
	}
	if _, err := st.Nfces().GetByID(ctx, targetNfceID); err == nil {
		t.Error("receipt still present after delete")
	}
}
