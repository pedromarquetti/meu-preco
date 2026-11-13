package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
	"meupreco/internal/testdb"
)

// handler tests run against the shared MEUPRECO_TEST_DATABASE_URL and use the
// schema reset performed once by TestMain. The router under test is the real
// one; only the bearer-token role varies.

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
	if err := testdb.Truncate(context.Background(), st.Pool()); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()

	user := &domain.User{Name: "Reader", Email: "reader@example.com", PasswordHash: "h", Role: "user"}
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
	if _, err := st.Products().Create(ctx, pub); err != nil {
		t.Fatalf("create public product: %v", err)
	}
	hid := &domain.Product{ProductKey: "hid", Name: "ERRO DE OCR", Unit: "UN", NeedsReview: true}
	if _, err := st.Products().Create(ctx, hid); err != nil {
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
	if err := testdb.Truncate(context.Background(), st.Pool()); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()

	user := &domain.User{Name: "Plain", Email: "plain@example.com", PasswordHash: "h", Role: "user"}
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

// TestAdminGetUser covers GET /admin/user/{id}: admins receive the safe user
// projection, while unknown ids, malformed ids and non-admins are rejected.
func TestAdminGetUser(t *testing.T) {
	st := integrationStore(t)
	if err := testdb.Truncate(context.Background(), st.Pool()); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()

	admin := &domain.User{Name: "Root", Email: "root@example.com", PasswordHash: "h", Role: "admin"}
	target := &domain.User{Name: "Alice", Email: "alice@example.com", PasswordHash: "h", Role: "user"}
	if err := st.Users().Create(ctx, admin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := st.Users().Create(ctx, target); err != nil {
		t.Fatalf("create target: %v", err)
	}

	api := roleRouter(t, st, pgResolver{}, admin.ID, "admin")
	rec, out := do(t, api, http.MethodGet, fmt.Sprintf("/api/v1/admin/user/%d", target.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get user = %d (%s)", rec.Code, rec.Body.String())
	}
	if jint(out["id"]) != target.ID || out["email"] != target.Email || out["role"] != "user" {
		t.Errorf("get user = %+v", out)
	}
	if _, leak := out["password_hash"]; leak {
		t.Errorf("get user leaked password_hash: %+v", out)
	}
	if out["created_at"] == nil {
		t.Errorf("get user missing created_at: %+v", out)
	}

	rec, out = do(t, api, http.MethodGet, "/api/v1/admin/user/999999", "")
	if rec.Code != http.StatusNotFound || out["error"].(map[string]any)["code"] != "not_found" {
		t.Errorf("missing user = %d (%s), want 404 not_found", rec.Code, rec.Body.String())
	}
	rec, out = do(t, api, http.MethodGet, "/api/v1/admin/user/0", "")
	if rec.Code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_filter" {
		t.Errorf("bad id = %d (%s), want 400 invalid_filter", rec.Code, rec.Body.String())
	}

	unpriv := roleRouter(t, st, pgResolver{}, target.ID, "user")
	rec, _ = do(t, unpriv, http.MethodGet, fmt.Sprintf("/api/v1/admin/user/%d", target.ID), "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("get user as non-admin = %d (%s), want 403", rec.Code, rec.Body.String())
	}
}

// TestAdminReviewQueueShowsProposedAssignment proves the admin queue carries
// the product a user proposed (plus who proposed it), while the pending row
// stays invisible to public product reads and non-admins cannot call the queue.
func TestAdminReviewQueueShowsProposedAssignment(t *testing.T) {
	st := integrationStore(t)
	if err := testdb.Truncate(context.Background(), st.Pool()); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()

	assigner := &domain.User{Name: "Assigner", Email: "assigner@example.com", PasswordHash: "h", Role: "user"}
	admin := &domain.User{Name: "Root", Email: "root@example.com", PasswordHash: "h", Role: "admin"}
	if err := st.Users().Create(ctx, assigner); err != nil {
		t.Fatalf("create assigner: %v", err)
	}
	if err := st.Users().Create(ctx, admin); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"}
	if err := st.Supermarkets().Upsert(ctx, sm); err != nil {
		t.Fatalf("upsert supermarket: %v", err)
	}

	proposed := &domain.Product{
		ProductKey:    "07891000412855",
		GTIN:          strptr("07891000412855"),
		Name:          "LIMAO TAHTI",
		Brand:         strptr("VIGOR"),
		Unit:          "KG",
		CanonicalStem: strptr("tijolo"),
	}
	if _, err := st.Products().Create(ctx, proposed); err != nil {
		t.Fatalf("create proposed product: %v", err)
	}

	issued := time.Date(2024, 11, 1, 8, 0, 0, 0, time.UTC)
	nf := &domain.Nfce{
		AccessKey:   fmt.Sprintf("35241152323688000144650020000002571115675%08d", httpNfceSeq.Add(1)),
		Supermarket: *sm, IssuedAt: &issued, CreatedBy: &assigner.ID,
	}
	if err := st.Nfces().Create(ctx, nf); err != nil {
		t.Fatalf("create nfce: %v", err)
	}
	assignNote := "assign"
	assigned := &domain.PriceLog{
		NfceID: nf.ID, ProductID: &proposed.ID, Line: 1,
		DescriptionSnapshot: "LIMAO TAHITI KG", StoreCode: "000222",
		Qty: decimal.NewFromFloat(1), Unit: "KG",
		UnitPrice: decimal.RequireFromString("5.99"), TotalPrice: decimal.RequireFromString("5.99"),
		IdentityStatus: "uid_uncertain", NeedsReview: true,
		ResolvedBy: &assigner.ID, ResolutionNote: &assignNote,
	}
	if err := st.PriceLogs().Create(ctx, assigned); err != nil {
		t.Fatalf("create assigned log: %v", err)
	}
	unresolved := seedHTTPLog(t, st, sm, "UN", nil, issued, "1.00", true, assigner.ID)

	api := roleRouter(t, st, pgResolver{}, admin.ID, "admin")
	rec, out := do(t, api, http.MethodGet, "/api/v1/admin/products/needs-review?limit=50", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("needs-review = %d (%s)", rec.Code, rec.Body.String())
	}
	items := out["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("queue = %d items, want unresolved + assigned (%s)", len(items), rec.Body.String())
	}

	var gotAssigned, gotUnresolved map[string]any
	for _, raw := range items {
		item := raw.(map[string]any)
		switch jint(item["price_log_id"]) {
		case assigned.ID:
			gotAssigned = item
		case unresolved.ID:
			gotUnresolved = item
		}
	}
	if gotAssigned == nil || gotUnresolved == nil {
		t.Fatalf("queue missing rows: assigned=%v unresolved=%v (%s)", gotAssigned != nil, gotUnresolved != nil, rec.Body.String())
	}

	proposal, ok := gotAssigned["proposed_product"].(map[string]any)
	if !ok {
		t.Fatalf("assigned row has no proposed_product: %+v", gotAssigned)
	}
	if jint(proposal["id"]) != proposed.ID || proposal["name"] != proposed.Name ||
		proposal["unit"] != proposed.Unit || proposal["product_key"] != proposed.ProductKey ||
		proposal["gtin"] != *proposed.GTIN || proposal["brand"] != *proposed.Brand ||
		proposal["canonical_stem"] != *proposed.CanonicalStem {
		t.Errorf("proposed_product = %+v", proposal)
	}
	if jint(gotAssigned["resolved_by"]) != assigner.ID {
		t.Errorf("resolved_by = %v, want %d", gotAssigned["resolved_by"], assigner.ID)
	}
	if gotAssigned["resolution_note"] != "assign" || gotAssigned["needs_review"] != true {
		t.Errorf("assigned audit fields = %+v", gotAssigned)
	}
	// Both rows have no event history in this fixture: proposal is pending and
	// the latest review action is absent (omitted by omitempty).
	if gotAssigned["proposal_status"] != "pending" || gotUnresolved["proposal_status"] != "pending" {
		t.Errorf("proposal_status = %v/%v, want pending/pending",
			gotAssigned["proposal_status"], gotUnresolved["proposal_status"])
	}
	if _, present := gotAssigned["last_review_action"]; present {
		t.Errorf("assigned row exposes last_review_action without events: %v", gotAssigned["last_review_action"])
	}
	if _, present := gotUnresolved["last_review_action"]; present {
		t.Errorf("unresolved row exposes last_review_action without events: %v", gotUnresolved["last_review_action"])
	}

	if _, present := gotUnresolved["proposed_product"]; present {
		t.Errorf("unresolved row exposes a proposed_product: %+v", gotUnresolved)
	}
	if _, present := gotUnresolved["resolved_by"]; present {
		t.Errorf("unresolved row exposes resolved_by: %+v", gotUnresolved)
	}

	// The pending proposal must not leak into public price reads.
	userAPI := roleRouter(t, st, pgResolver{}, assigner.ID, "user")
	rec, _ = do(t, userAPI, http.MethodGet, fmt.Sprintf("/api/v1/products/%d/history", proposed.ID), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("product history = %d (%s)", rec.Code, rec.Body.String())
	}
	rec, out = do(t, userAPI, http.MethodGet, fmt.Sprintf("/api/v1/supermarkets/%d/products", sm.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("supermarket products = %d (%s)", rec.Code, rec.Body.String())
	}
	if items, _ := out["items"].([]any); len(items) != 0 {
		t.Errorf("pending proposal leaked into supermarket products: %v (%s)", out["items"], rec.Body.String())
	}

	// Public clients cannot call the queue: 403 as a user, 401 unauthenticated.
	rec, _ = do(t, userAPI, http.MethodGet, "/api/v1/admin/products/needs-review", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("user queue access = %d, want 403", rec.Code)
	}
	raw := newRouter(config.Config{JWTSecret: testJWTSecret, CORSOrigins: []string{"https://app.example.com"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)), pgResolver{}, st, nil)
	rec, _ = do(t, raw, http.MethodGet, "/api/v1/admin/products/needs-review", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous queue access = %d, want 401", rec.Code)
	}
}

// TestAdminReviewLifecycleReassignment walks the public admin contract through
// assign -> reject -> reassign -> approve, asserting queue visibility and the
// proposal_status/last_review_action fields at every step.
func TestAdminReviewLifecycleReassignment(t *testing.T) {
	st := integrationStore(t)
	if err := testdb.Truncate(context.Background(), st.Pool()); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()

	admin := &domain.User{Name: "Root", Email: "root@example.com", PasswordHash: "h", Role: "admin"}
	if err := st.Users().Create(ctx, admin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"}
	if err := st.Supermarkets().Upsert(ctx, sm); err != nil {
		t.Fatalf("upsert supermarket: %v", err)
	}
	productA := &domain.Product{ProductKey: "banana-a", Name: "BANANA A", Unit: "KG"}
	productB := &domain.Product{ProductKey: "banana-b", Name: "BANANA B", Unit: "KG"}
	for _, p := range []*domain.Product{productA, productB} {
		if _, err := st.Products().Create(ctx, p); err != nil {
			t.Fatalf("create product: %v", err)
		}
	}

	log := seedHTTPLog(t, st, sm, "KG", nil, time.Date(2024, 11, 4, 8, 0, 0, 0, time.UTC), "5.00", true, admin.ID)
	api := roleRouter(t, st, pgResolver{}, admin.ID, "admin")

	queue := func() ([]any, int64) {
		t.Helper()
		rec, out := do(t, api, http.MethodGet, "/api/v1/admin/products/needs-review?limit=50", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("queue = %d (%s)", rec.Code, rec.Body.String())
		}
		return out["items"].([]any), jint(out["page"].(map[string]any)["total"])
	}
	assign := func(productID int64) {
		t.Helper()
		rec, out := do(t, api, http.MethodPost, "/api/v1/admin/products/batch-assign",
			fmt.Sprintf(`{"assignments":[{"id":%d,"action":"existing","product_id":%d}]}`, log.ID, productID))
		if rec.Code != http.StatusOK {
			t.Fatalf("assign %d = %d (%s)", productID, rec.Code, rec.Body.String())
		}
		if jint(out["assigned"]) != 1 {
			t.Errorf("assign %d result = %+v", productID, out)
		}
	}
	review := func(action string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		return do(t, api, http.MethodPost,
			fmt.Sprintf("/api/v1/admin/products/price-logs/%d/review", log.ID),
			fmt.Sprintf(`{"action":%q}`, action))
	}

	// Initial state: an unresolved row with no proposal or event history.
	items, total := queue()
	if total != 1 || len(items) != 1 {
		t.Fatalf("initial queue = %d items (total %d), want 1 (%+v)", len(items), total, items)
	}
	item := items[0].(map[string]any)
	if item["proposal_status"] != "pending" || item["needs_review"] != true {
		t.Errorf("initial item = %+v, want pending/needs_review", item)
	}
	if _, present := item["proposed_product"]; present {
		t.Errorf("unresolved item exposes proposed_product: %+v", item)
	}
	if _, present := item["last_review_action"]; present {
		t.Errorf("unresolved item exposes last_review_action: %+v", item)
	}

	// A decision without a proposal is invalid and writes nothing.
	rec, out := review("reject")
	if rec.Code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "bad_request" {
		t.Fatalf("reject unresolved = %d (%s), want 400 bad_request", rec.Code, rec.Body.String())
	}
	if items, _ = queue(); len(items) != 1 {
		t.Fatalf("failed rejection changed the queue: %+v", items)
	}
	if it := items[0].(map[string]any); it["proposal_status"] != "pending" {
		t.Errorf("failed rejection changed proposal_status: %+v", it)
	}

	// First assignment: visible and pending.
	assign(productA.ID)
	items, total = queue()
	if total != 1 || len(items) != 1 {
		t.Fatalf("queue after assign A = %d items (total %d), want 1", len(items), total)
	}
	item = items[0].(map[string]any)
	if item["proposal_status"] != "pending" || item["last_review_action"] != "assign" || item["needs_review"] != true {
		t.Errorf("assigned item = %+v, want pending/assign/needs_review", item)
	}
	if pp, ok := item["proposed_product"].(map[string]any); !ok || jint(pp["id"]) != productA.ID {
		t.Errorf("proposed_product = %+v, want A (%d)", item["proposed_product"], productA.ID)
	}

	// Rejection: still visible, still unpublished, reports rejected.
	if rec, _ = review("reject"); rec.Code != http.StatusOK {
		t.Fatalf("reject = %d (%s)", rec.Code, rec.Body.String())
	}
	items, total = queue()
	if total != 1 || len(items) != 1 {
		t.Fatalf("queue after reject = %d items (total %d), want 1", len(items), total)
	}
	item = items[0].(map[string]any)
	if item["proposal_status"] != "rejected" || item["last_review_action"] != "reject" || item["needs_review"] != true {
		t.Errorf("rejected item = %+v, want rejected/reject/needs_review", item)
	}
	if pp, ok := item["proposed_product"].(map[string]any); !ok || jint(pp["id"]) != productA.ID {
		t.Errorf("rejection hid the proposal: %+v", item)
	}

	// Repeating the same rejection is a conflict.
	rec, out = review("reject")
	if rec.Code != http.StatusConflict || out["error"].(map[string]any)["code"] != "already_processed" {
		t.Fatalf("duplicate reject = %d (%s), want 409 already_processed", rec.Code, rec.Body.String())
	}

	// Reassignment resets the proposal to pending with product B.
	assign(productB.ID)
	items, total = queue()
	if total != 1 || len(items) != 1 {
		t.Fatalf("queue after assign B = %d items (total %d), want 1", len(items), total)
	}
	item = items[0].(map[string]any)
	if item["proposal_status"] != "pending" || item["last_review_action"] != "assign" {
		t.Errorf("reassigned item = %+v, want pending/assign", item)
	}
	if pp, ok := item["proposed_product"].(map[string]any); !ok || jint(pp["id"]) != productB.ID {
		t.Errorf("proposed_product after reassignment = %+v, want B (%d)", item["proposed_product"], productB.ID)
	}

	// Approval publishes the row and removes it from the queue.
	if rec, _ = review("approve"); rec.Code != http.StatusOK {
		t.Fatalf("approve = %d (%s)", rec.Code, rec.Body.String())
	}
	if items, total = queue(); total != 0 || len(items) != 0 {
		t.Errorf("queue after approve = %d items (total %d), want empty", len(items), total)
	}

	latest, err := st.Events().LatestByPriceLogID(ctx, log.ID)
	if err != nil {
		t.Fatalf("latest event: %v", err)
	}
	if latest.Action != "approve" {
		t.Errorf("latest event action = %q, want approve", latest.Action)
	}
}

// TestAdminPatchUnpublishedProduct proves PATCH /admin/products/{id} uses the
// write lookup: a product without any approved price log is invisible to the
// public read but remains a valid curation target, and the change persists.
func TestAdminPatchUnpublishedProduct(t *testing.T) {
	st := integrationStore(t)
	if err := testdb.Truncate(context.Background(), st.Pool()); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()

	admin := &domain.User{Name: "Root", Email: "root@example.com", PasswordHash: "h", Role: "admin"}
	if err := st.Users().Create(ctx, admin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	api := roleRouter(t, st, pgResolver{}, admin.ID, "admin")

	pending := &domain.Product{ProductKey: "pending-apresuntado", Name: "APRESUNTADO", Unit: "KG"}
	if _, err := st.Products().Create(ctx, pending); err != nil {
		t.Fatalf("create pending product: %v", err)
	}

	rec, out := do(t, api, http.MethodPatch, fmt.Sprintf("/api/v1/admin/products/%d", pending.ID),
		`{"name":"APRESUNTADO DE PERU","brand":"SEARA"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch pending product = %d (%s)", rec.Code, rec.Body.String())
	}
	if out["name"] != "APRESUNTADO DE PERU" || out["brand"] != "SEARA" || out["unit"] != "KG" {
		t.Errorf("patched product = %+v", out)
	}

	got, err := st.Products().GetByIDForWrite(ctx, pending.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Name != "APRESUNTADO DE PERU" || got.Brand == nil || *got.Brand != "SEARA" {
		t.Errorf("stored product = %+v, want the patched fields", got)
	}

	// The product is still unpublished: public reads continue to hide it.
	userAPI := roleRouter(t, st, pgResolver{}, admin.ID, "user")
	rec, _ = do(t, userAPI, http.MethodGet, fmt.Sprintf("/api/v1/products/%d", pending.ID), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("public product read = %d, want 404 for a pending-only product", rec.Code)
	}
}

// TestAdminMergeAndDeleteUnpublishedProducts proves the admin merge and delete
// endpoints resolve unpublished products through the write lookup.
func TestAdminMergeAndDeleteUnpublishedProducts(t *testing.T) {
	st := integrationStore(t)
	if err := testdb.Truncate(context.Background(), st.Pool()); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()

	admin := &domain.User{Name: "Root", Email: "root@example.com", PasswordHash: "h", Role: "admin"}
	if err := st.Users().Create(ctx, admin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	api := roleRouter(t, st, pgResolver{}, admin.ID, "admin")
	publicAPI := roleRouter(t, st, pgResolver{}, admin.ID, "user")

	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"}
	if err := st.Supermarkets().Upsert(ctx, sm); err != nil {
		t.Fatalf("upsert sm: %v", err)
	}
	base := time.Date(2024, 11, 1, 8, 0, 0, 0, time.UTC)

	// Merge: both sides are pending-only (no approved price log), so the public
	// read hides them; the admin merge must still resolve them.
	from := &domain.Product{ProductKey: "pending-manteiga", Name: "MANTEIGA", Unit: "KG"}
	to := &domain.Product{ProductKey: "pending-manteiga-sal", Name: "MANTEIGA COM SAL", Unit: "KG"}
	for _, p := range []*domain.Product{from, to} {
		if _, err := st.Products().Create(ctx, p); err != nil {
			t.Fatalf("create product %q: %v", p.Name, err)
		}
	}
	fromLog := seedHTTPLog(t, st, sm, "KG", from, base, "9.00", true, admin.ID)
	if err := st.PriceLogs().SetProductProposal(ctx, fromLog.ID, from.ID, admin.ID, "assign"); err != nil {
		t.Fatalf("propose product: %v", err)
	}

	for _, p := range []*domain.Product{from, to} {
		rec, _ := do(t, publicAPI, http.MethodGet, fmt.Sprintf("/api/v1/products/%d", p.ID), "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("public read %q = %d, want 404 for a pending-only product", p.Name, rec.Code)
		}
	}

	rec, out := do(t, api, http.MethodPost, "/api/v1/admin/products/merge",
		fmt.Sprintf(`{"from_id":%d,"to_id":%d}`, from.ID, to.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("merge pending-only products = %d (%s)", rec.Code, rec.Body.String())
	}
	if jint(out["logs_moved"]) != 1 || out["dup_removed"] != true {
		t.Errorf("merge result = %+v", out)
	}
	if _, err := st.Products().GetByIDForWrite(ctx, from.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("merged-away product still present: %v", err)
	}
	rec, _ = do(t, publicAPI, http.MethodGet, fmt.Sprintf("/api/v1/products/%d", to.ID), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("public read after merge = %d, want 404 (still unpublished)", rec.Code)
	}

	// Delete a pending-only product with no logs.
	empty := &domain.Product{ProductKey: "pending-apresuntado", Name: "APRESUNTADO", Unit: "KG"}
	if _, err := st.Products().Create(ctx, empty); err != nil {
		t.Fatalf("create empty product: %v", err)
	}
	rec, out = do(t, api, http.MethodDelete, fmt.Sprintf("/api/v1/admin/products/%d", empty.ID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete pending-only product = %d (%s)", rec.Code, rec.Body.String())
	}
	if jint(out["logs_deleted"]) != 0 {
		t.Errorf("logs_deleted = %v, want 0", out["logs_deleted"])
	}
	if _, err := st.Products().GetByIDForWrite(ctx, empty.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleted product still present: %v", err)
	}

	// Delete a pending-only product that owns a log: guarded without purge,
	// then dropped together with its logs.
	purge := &domain.Product{ProductKey: "pending-purge", Name: "MORTADELA", Unit: "KG"}
	if _, err := st.Products().Create(ctx, purge); err != nil {
		t.Fatalf("create purge product: %v", err)
	}
	purgeLog := seedHTTPLog(t, st, sm, "KG", purge, base.Add(time.Hour), "15.00", true, admin.ID)
	if err := st.PriceLogs().SetProductProposal(ctx, purgeLog.ID, purge.ID, admin.ID, "assign"); err != nil {
		t.Fatalf("propose purge product: %v", err)
	}
	rec, out = do(t, api, http.MethodDelete, fmt.Sprintf("/api/v1/admin/products/%d", purge.ID), "")
	if rec.Code != http.StatusConflict || out["error"].(map[string]any)["code"] != "in_use" {
		t.Errorf("guarded delete = %d (%s)", rec.Code, rec.Body.String())
	}
	rec, out = do(t, api, http.MethodDelete, fmt.Sprintf("/api/v1/admin/products/%d?purge=true", purge.ID), "")
	if rec.Code != http.StatusOK || jint(out["logs_deleted"]) != 1 {
		t.Errorf("purge = %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := st.Products().GetByIDForWrite(ctx, purge.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("purged product still present: %v", err)
	}
}

func TestAdminCuration(t *testing.T) {
	st := integrationStore(t)
	if err := testdb.Truncate(context.Background(), st.Pool()); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()

	admin := &domain.User{Name: "Root", Email: "root@example.com", PasswordHash: "h", Role: "admin"}
	other := &domain.User{Name: "Other", Email: "other@example.com", PasswordHash: "h", Role: "user"}
	if err := st.Users().Create(ctx, admin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := st.Users().Create(ctx, other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	api := roleRouter(t, st, pgResolver{}, admin.ID, "admin")
	publicAPI := roleRouter(t, st, pgResolver{}, admin.ID, "user")
	pending := &domain.Product{ProductKey: "pending-admin-search", Name: "PENDING ADMIN SEARCH", Unit: "KG"}
	if _, err := st.Products().Create(ctx, pending); err != nil {
		t.Fatalf("create pending search product: %v", err)
	}
	rec, out := do(t, api, http.MethodGet, "/api/v1/admin/products?q=pending+admin&limit=20", "")
	if rec.Code != http.StatusOK || jint(out["page"].(map[string]any)["total"]) != 1 {
		t.Fatalf("admin product search = %d (%s), want pending product", rec.Code, rec.Body.String())
	}
	if got := out["items"].([]any)[0].(map[string]any); jint(got["id"]) != pending.ID {
		t.Errorf("admin search item = %+v, want %d", got, pending.ID)
	}
	rec, publicOut := do(t, publicAPI, http.MethodGet, "/api/v1/products?q=pending+admin&limit=20", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("public product search = %d (%s)", rec.Code, rec.Body.String())
	}
	if total := jint(publicOut["page"].(map[string]any)["total"]); total != 0 {
		t.Errorf("public search total = %d, want 0", total)
	}
	rec, _ = do(t, publicAPI, http.MethodGet, "/api/v1/admin/products?q=pending+admin&limit=20", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin product search = %d, want 403", rec.Code)
	}

	sm := &domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"}
	if err := st.Supermarkets().Upsert(ctx, sm); err != nil {
		t.Fatalf("upsert sm: %v", err)
	}
	pcur := &domain.Product{ProductKey: "prod-arroz", Name: "ARROZ BRANCO TIPO 1", Unit: "UN"}
	if _, err := st.Products().Create(ctx, pcur); err != nil {
		t.Fatalf("create product: %v", err)
	}
	base := time.Date(2024, 11, 1, 8, 0, 0, 0, time.UTC)
	reviewLog := seedHTTPLog(t, st, sm, "KG", pcur, base, "5.00", true, admin.ID)

	// mdup is the duplicate product a merge folds into pcur.
	mdup := &domain.Product{ProductKey: "prod-arroz-dupe", Name: "ARROZ BRANCO 5KG", Unit: "UN"}
	if _, err := st.Products().Create(ctx, mdup); err != nil {
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
	rec, out = do(t, api, http.MethodGet, "/api/v1/admin/products/needs-review?limit=50", "")
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

	// 2. Batch-assign records a pending proposal: the queue keeps the row with
	// the proposed product until an explicit approve publishes it.
	rec, out = do(t, api, http.MethodPost, "/api/v1/admin/products/batch-assign",
		fmt.Sprintf(`{"assignments":[{"id":%d,"action":"create","name":"MAÇA FUJI","brand":"IMPORTADA","unit":"kg"}]}`, reviewLog.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("batch-assign = %d (%s)", rec.Code, rec.Body.String())
	}
	if jint(out["assigned"]) != 1 || jint(out["created"]) != 1 {
		t.Errorf("batch result = %+v", out)
	}
	rec, out = do(t, api, http.MethodGet, "/api/v1/admin/products/needs-review?limit=50", "")
	if rec.Code != http.StatusOK || jint(out["page"].(map[string]any)["total"]) != 1 {
		t.Errorf("queue after assign = %d (%s), want the pending proposal", rec.Code, rec.Body.String())
	}
	pend := out["items"].([]any)[0].(map[string]any)
	if pend["proposal_status"] != "pending" || pend["last_review_action"] != "assign" || pend["needs_review"] != true {
		t.Errorf("pending proposal = %+v", pend)
	}
	if pp, ok := pend["proposed_product"].(map[string]any); !ok || jint(pp["id"]) == 0 {
		t.Errorf("pending proposal missing product: %+v", pend)
	}

	// Approve publishes the proposal and empties the queue. The response is the
	// reviewed product, including its brand.
	rec, out = do(t, api, http.MethodPost, fmt.Sprintf("/api/v1/admin/products/price-logs/%d/review", reviewLog.ID),
		`{"action":"approve"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve = %d (%s)", rec.Code, rec.Body.String())
	}
	if jint(out["id"]) == 0 || out["name"] != "MAÇA FUJI" || out["brand"] != "IMPORTADA" {
		t.Errorf("approve result = %+v, want the reviewed product with its brand", out)
	}
	rec, out = do(t, api, http.MethodGet, "/api/v1/admin/products/needs-review?limit=50", "")
	if rec.Code != http.StatusOK || jint(out["page"].(map[string]any)["total"]) != 0 {
		t.Errorf("queue after approve = %d (%s)", rec.Code, rec.Body.String())
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
			foundAdmin = m["role"] == "admin" && m["name"] == "Root"
		case "other@example.com":
			foundOther = m["role"] == "user" && m["name"] == "Other"
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
