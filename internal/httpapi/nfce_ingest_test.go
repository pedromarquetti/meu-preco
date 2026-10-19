package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"meupreco/internal/auth"
	"meupreco/internal/config"
	"meupreco/internal/domain"
	"meupreco/internal/nfce"
	"meupreco/internal/product"
	"meupreco/internal/service"
	pstore "meupreco/internal/store/postgres"
	"meupreco/migrations"
)

// Two-phase ingestion end-to-end (POST /api/v1/nfce/new + /nfce/submit)
// against a real postgres store. The phase DoD "needs_review invisible to
// public GET /api/v1/products" is asserted here at the data level (product_id
// NULL + needs_review flag); the public read endpoint itself filters them out.
//
// This suite shares the MEUPRECO_TEST_DATABASE_URL database with the
// store/postgres package. It never migrates and only truncates the rows it
// touches, so parallel runs stay self-contained.

type pgResolver struct{ receipt *domain.Nfce }

func (r pgResolver) Resolve(_ context.Context, _ nfce.AccessKey, _ string) (*domain.Nfce, error) {
	return r.receipt, nil
}

// schemaLockKey mirrors internal/store/postgres: a handful of test packages
// share MEUPRECO_TEST_DATABASE_URL, so schema mutations acquired while holding
// this advisory lock never interleave (Down+Up vs Up-only). This package holds
// the lock for its entire run so a late-starting store resetSchema cannot drop
// tables mid-test.
const schemaLockKey int64 = 0x6d6575707265636f // "meupreco"

func integrationStore(t *testing.T) *pstore.Store {
	t.Helper()
	dsn := os.Getenv("MEUPRECO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("MEUPRECO_TEST_DATABASE_URL not set; skipping postgres-backed integration test")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("test database unreachable: %v", err)
	}

	// Hold the schema lock on a dedicated session for the whole test so no
	// other package resets migrations underneath us; Up (not Down) so we never
	// destroy data another package is using.
	lock, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire lock session: %v", err)
	}
	if _, err := lock.Exec(context.Background(),
		`SELECT pg_advisory_lock($1)`, schemaLockKey); err != nil {
		t.Fatalf("advisory lock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = lock.Exec(context.Background(),
			`SELECT pg_advisory_unlock($1)`, schemaLockKey)
		lock.Release()
		pool.Close()
	})

	if err := migrateSchemaUp(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	return pstore.NewFromPool(pool)
}

// migrateSchemaUp applies pending migrations (Up only, no-op when current). It
// is advisory-locked by the caller so it is safe to run while the store
// package's resetSchema is waiting on the same lock.
func migrateSchemaUp(dsn string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func truncatePG(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`TRUNCATE previews, price_logs, nfces, products, supermarkets, users RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// ingestionRouter builds the full router against a real pg store and a stub
// resolver; userID is who the bearer token belongs to.
func ingestionRouter(t *testing.T, stores domain.Stores, res nfceResolver, userID int64) http.Handler {
	t.Helper()
	return authRouter(t, stores, res, userID, nil)
}

// authRouter is the shared router harness: the config, a signed bearer token
// for userID, and (optionally) an injected captcha support. captcha nil picks
// the default newCaptchaSupport wiring.
func authRouter(t *testing.T, stores domain.Stores, res nfceResolver, userID int64, captcha *service.CaptchaSupport) http.Handler {
	t.Helper()
	cfg := config.Config{
		CORSOrigins:   []string{"https://app.example.com"},
		JWTSecret:     testJWTSecret,
		JWTAccessTTL:  30 * time.Minute,
		JWTRefreshTTL: 24 * time.Hour,
	}
	tokens := auth.NewTokenService(cfg.JWTSecret, cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
	u := &domain.User{ID: userID, Email: "user@example.com", Role: "user"}
	token, err := tokens.IssueAccess(u)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	handler := newRouter(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), res, stores, captcha)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		handler.ServeHTTP(w, r)
	})
}

func mustAddPGProduct(t *testing.T, st *pstore.Store) {
	t.Helper()
	p := &domain.Product{
		ProductKey: "07891000412855",
		GTIN:       strptr("07891000412855"),
		Name:       "ACHOCOLATADO EM PO NESCAU 350G",
		Unit:       "GR",
	}
	if err := st.Products().Create(context.Background(), p); err != nil {
		t.Fatalf("seed product: %v", err)
	}
}

func strptr(s string) *string { return &s }

func strconvItoa(i int64) string {
	return strconv.FormatInt(i, 10)
}

// ingestReceipt is the two-user fixture: a barcoded NESCAU (gtin_known) plus
// a GTIN-less "saco de lixo" line (uid_uncertain). It is the exact receipt
// both users "scan".
func ingestReceipt() *domain.Nfce {
	return &domain.Nfce{
		AccessKey:     senateKey,
		Supermarket:   domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"},
		Number:        "257",
		Series:        "002",
		Protocol:      "135241234567890",
		Environment:   "Producao",
		PaymentMethod: "Cartao de Credito",
		Total:         decimal.RequireFromString("59.06"),
		Items: []domain.NfceItem{
			{
				Line: 1, Code: "7891000412855", Description: "ACHOCOLATADO EM PO NESCAU 350G",
				Qty: decimal.RequireFromString("1"), Unit: "UN",
				UnitPrice: decimal.RequireFromString("22.99"), TotalPrice: decimal.RequireFromString("22.99"),
			},
			{
				Line: 2, Code: "", Description: "SACO DE LIXO 30L PRETO",
				Qty: decimal.RequireFromString("2"), Unit: "UN",
				UnitPrice: decimal.RequireFromString("3.49"), TotalPrice: decimal.RequireFromString("6.98"),
			},
		},
	}
}

func TestNfceTwoPhaseIngestion(t *testing.T) {
	st := integrationStore(t)
	if st == nil {
		return
	}
	truncatePG(t, st.Pool())
	mustAddPGProduct(t, st)

	ctx := context.Background()
	u1 := &domain.User{Name: "User One", Email: "user@example.com", PasswordHash: "h", Role: "user"}
	if err := st.Users().Create(ctx, u1); err != nil {
		t.Fatalf("create user 1: %v", err)
	}
	u2 := &domain.User{Name: "User Two", Email: "user2@example.com", PasswordHash: "h", Role: "user"}
	if err := st.Users().Create(ctx, u2); err != nil {
		t.Fatalf("create user 2: %v", err)
	}

	receipt := ingestReceipt()
	routerA := ingestionRouter(t, st, pgResolver{receipt: receipt}, u1.ID)
	routerB := ingestionRouter(t, st, pgResolver{receipt: receipt}, u2.ID)

	do := func(router http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		var out map[string]any
		if rec.Body.Len() > 0 {
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
		}
		return rec, out
	}

	// 1. new builds the itemized plan and persists only a pending row.
	rec, plan := do(routerA, http.MethodPost, "/api/v1/nfce/new",
		`{"access_key":"`+senateKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("new status = %d, body = %s", rec.Code, rec.Body.String())
	}
	previewID := int64(plan["preview_id"].(float64))
	if previewID == 0 {
		t.Fatal("preview_id not returned")
	}
	items := plan["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("plan items = %d, want 2", len(items))
	}
	var gtinKnown, uidUncertain int
	for _, it := range items {
		switch it.(map[string]any)["status"] {
		case "gtin_known":
			gtinKnown++
		case "uid_uncertain":
			uidUncertain++
		}
	}
	if gtinKnown != 1 || uidUncertain != 1 {
		t.Errorf("plan breakdown = %d gtin_known / %d uid_uncertain", gtinKnown, uidUncertain)
	}

	assertCounts := func(wantNfce, wantLog, wantSuper int) {
		t.Helper()
		var nf, lg, su int64
		if err := st.Pool().QueryRow(context.Background(),
			`SELECT (SELECT count(*) FROM nfces), (SELECT count(*) FROM price_logs), (SELECT count(*) FROM supermarkets)`).Scan(&nf, &lg, &su); err != nil {
			t.Fatalf("count: %v", err)
		}
		if nf != int64(wantNfce) || lg != int64(wantLog) || su != int64(wantSuper) {
			t.Errorf("db rows = nfces:%d logs:%d supermarkets:%d, want %d/%d/%d",
				nf, lg, su, wantNfce, wantLog, wantSuper)
		}
	}

	// 2. Re-new-ing the same receipt while pending replays the same draft.
	rec, replay := do(routerA, http.MethodPost, "/api/v1/nfce/new",
		`{"access_key":"`+senateKey+`"}`)
	if rec.Code != http.StatusOK || int64(replay["preview_id"].(float64)) != previewID {
		t.Errorf("replay: status=%d preview=%v want %d", rec.Code, replay["preview_id"], previewID)
	}

	// 3. Dry run writes nothing.
	_, out := do(routerA, http.MethodPost, "/api/v1/nfce/submit",
		`{"preview_id":`+strconvItoa(previewID)+`,"confirm":false}`)
	if out["confirmed"] != false {
		t.Errorf("dry run result = %v", out)
	}
	assertCounts(0, 0, 0)

	// 4. Confirm persists supermarket + nfce + the gtin_known autosave. The
	// GTIN-less "saco de lixo" line is explicitly skipped, so it writes nothing.
	_, out = do(routerA, http.MethodPost, "/api/v1/nfce/submit",
		`{"preview_id":`+strconvItoa(previewID)+`,"confirm":true,"resolutions":[{"line":2,"action":"skip"}]}`)
	if out["written"].(float64) != 1 || out["auto_saved"].(float64) != 1 || out["skipped"].(float64) != 1 || out["needs_review"].(float64) != 0 {
		t.Errorf("commit result = %v", out)
	}
	assertCounts(1, 1, 1)

	var logRow struct {
		ProductID      *int64
		IdentityStatus string
		NeedsReview    bool
	}
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT product_id, identity_status, needs_review FROM price_logs WHERE line = 1`).
		Scan(&logRow.ProductID, &logRow.IdentityStatus, &logRow.NeedsReview); err != nil {
		t.Fatalf("scan log: %v", err)
	}
	if logRow.ProductID == nil || logRow.IdentityStatus != "gtin_known" || logRow.NeedsReview {
		t.Errorf("line-1 log = product:%v status:%s review:%v", logRow.ProductID, logRow.IdentityStatus, logRow.NeedsReview)
	}

	// 5. Double-commit of the SAME preview_id is the 409 ("wire-retry" case).
	rec, _ = do(routerA, http.MethodPost, "/api/v1/nfce/submit",
		`{"preview_id":`+strconvItoa(previewID)+`,"confirm":true}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("double commit status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}

	// 6. Cross-user re-submit: user B drafts the same receipt and confirms.
	// Both endpoints answer 200; only user A's rows exist. This is the
	// "two users scan the same nfce" guarantee — the DB holds ONE receipt.
	recB, planB := do(routerB, http.MethodPost, "/api/v1/nfce/new",
		`{"access_key":"`+senateKey+`"}`)
	if recB.Code != http.StatusOK {
		t.Fatalf("user B new status = %d", recB.Code)
	}
	previewB := int64(planB["preview_id"].(float64))
	if previewB == previewID {
		t.Fatalf("user B replayed user A's preview id %d, want an independent draft", previewB)
	}
	recB, outB := do(routerB, http.MethodPost, "/api/v1/nfce/submit",
		`{"preview_id":`+strconvItoa(previewB)+`,"confirm":true,"resolutions":[{"line":2,"action":"skip"}]}`)
	if recB.Code != http.StatusOK {
		t.Fatalf("user B submit status = %d, body = %s", recB.Code, recB.Body.String())
	}
	if outB["written"].(float64) != 0 {
		t.Errorf("user B wrote %v rows, want 0 (silent dedup)", outB["written"])
	}
	if int64(outB["nfce_id"].(float64)) != int64(out["nfce_id"].(float64)) {
		t.Errorf("user B nfce_id = %v, want user A's %v (shared receipt)", outB["nfce_id"], out["nfce_id"])
	}
	assertCounts(1, 1, 1)

	// 7. A request that omits resolutions must default the uid_uncertain line
	// to needs_review: a private (product-less) row is written and the counter
	// reflects it. This is the backend-enforced safe default for API clients.
	rec, plan3 := do(routerA, http.MethodPost, "/api/v1/nfce/new",
		`{"access_key":"`+senateKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("third draft status = %d", rec.Code)
	}
	preview3 := int64(plan3["preview_id"].(float64))
	_, out = do(routerA, http.MethodPost, "/api/v1/nfce/submit",
		`{"preview_id":`+strconvItoa(preview3)+`,"confirm":true}`)
	if out["needs_review"].(float64) != 1 || out["skipped"].(float64) != 1 || out["written"].(float64) != 1 {
		t.Errorf("default-resolution result = %v, want 1 written / 1 needs_review / 1 skipped(dup)", out)
	}
	assertCounts(1, 2, 1)

	var review struct {
		ProductID      *int64
		IdentityStatus string
		NeedsReview    bool
		ResolutionNote *string
	}
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT product_id, identity_status, needs_review, resolution_note FROM price_logs WHERE line = 2`).
		Scan(&review.ProductID, &review.IdentityStatus, &review.NeedsReview, &review.ResolutionNote); err != nil {
		t.Fatalf("scan review: %v", err)
	}
	if review.ProductID != nil || !review.NeedsReview {
		t.Errorf("needs_review row = product:%v needs_review:%v", review.ProductID, review.NeedsReview)
	}
	if review.IdentityStatus != "uid_uncertain" {
		t.Errorf("identity_status = %q, want uid_uncertain", review.IdentityStatus)
	}
	if review.ResolutionNote == nil || *review.ResolutionNote != "needs_review" {
		t.Errorf("resolution_note = %v, want needs_review", review.ResolutionNote)
	}

	// 8. Assign to a non-existent product is a clean 400.
	rec, plan4 := do(routerA, http.MethodPost, "/api/v1/nfce/new",
		`{"access_key":"`+senateKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("fourth draft status = %d", rec.Code)
	}
	preview4 := int64(plan4["preview_id"].(float64))
	rec, _ = do(routerA, http.MethodPost, "/api/v1/nfce/submit",
		`{"preview_id":`+strconvItoa(preview4)+`,"confirm":true,"resolutions":[{"line":2,"action":"assign","product_id":999999}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("assign-missing status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}

	// 9. Expired preview is a 422.
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE previews SET ttl = now() - interval '1 minute' WHERE id = $1`, preview4); err != nil {
		t.Fatalf("expire: %v", err)
	}
	rec, _ = do(routerA, http.MethodPost, "/api/v1/nfce/submit",
		`{"preview_id":`+strconvItoa(preview4)+`,"confirm":true}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("expired submit status = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
}

// TestNfceCommitPersistsCreatedProductIDs pins the product↔price_log
// relationship for the auto-save tiers. ensureProduct used to return p.ID in
// the same statement that created the row (`return p.ID, Create(...)`); Go does
// not order the non-call operand p.ID against the Create call, so a compiler is
// free to read the pre-scan id (0) and hand price_logs a 0 product_id, which
// trips the foreign key and rolls back the whole receipt. The fix splits the
// create from the return. This drives a receipt whose two lines are a brand-new
// barcode and a GTIN-less commodity through the real postgres commit path and
// asserts each price_log joins to the product row its identity created.
func TestNfceCommitPersistsCreatedProductIDs(t *testing.T) {
	st := integrationStore(t)
	if st == nil {
		return
	}
	truncatePG(t, st.Pool())

	ctx := context.Background()
	u := &domain.User{Name: "User", Email: "user@example.com", PasswordHash: "h", Role: "user"}
	if err := st.Users().Create(ctx, u); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Line 1: a barcode absent from the catalog → gtin_new (creates a product).
	// Line 2: a GTIN-less commodity the seed vocabulary knows → the
	// commodity_fingerprint tier (also creates a product).
	receipt := &domain.Nfce{
		AccessKey:     senateKey,
		Supermarket:   domain.Supermarket{CNPJ: "12345678000199", LegalName: "MERCADO BOM PRECO LTDA"},
		Number:        "300",
		Series:        "001",
		Protocol:      "135241234567890",
		Environment:   "Producao",
		PaymentMethod: "Dinheiro",
		Total:         decimal.RequireFromString("21.99"),
		Items: []domain.NfceItem{
			{
				Line: 1, Code: "7896008038717", Description: "ARROZ INTEGRAL TIO JOAO 1KG",
				Qty: decimal.RequireFromString("1"), Unit: "UN",
				UnitPrice: decimal.RequireFromString("9.99"), TotalPrice: decimal.RequireFromString("9.99"),
			},
			{
				Line: 2, Code: "", Description: "BANANA PRATA",
				Qty: decimal.RequireFromString("1.5"), Unit: "KG",
				UnitPrice: decimal.RequireFromString("8.00"), TotalPrice: decimal.RequireFromString("12.00"),
			},
		},
	}
	router := ingestionRouter(t, st, pgResolver{receipt: receipt}, u.ID)

	do := func(method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		var out map[string]any
		if rec.Body.Len() > 0 {
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
		}
		return rec, out
	}

	rec, plan := do(http.MethodPost, "/api/v1/nfce/new", `{"access_key":"`+senateKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("new status = %d, body = %s", rec.Code, rec.Body.String())
	}
	statuses := map[string]int{}
	for _, it := range plan["items"].([]any) {
		statuses[it.(map[string]any)["status"].(string)]++
	}
	if statuses["gtin_new"] != 1 || statuses["commodity_fingerprint"] != 1 {
		t.Fatalf("plan statuses = %v, want 1 gtin_new + 1 commodity_fingerprint", statuses)
	}

	previewID := int64(plan["preview_id"].(float64))
	rec, out := do(http.MethodPost, "/api/v1/nfce/submit",
		`{"preview_id":`+strconvItoa(previewID)+`,"confirm":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if out["written"].(float64) != 2 || out["auto_saved"].(float64) != 2 {
		t.Errorf("commit result = %v, want 2 written / 2 auto_saved", out)
	}

	gtinKey, ok := product.NormalizeGTIN("7896008038717")
	if !ok {
		t.Fatal("test fixture GTIN failed to normalize")
	}
	commodityKey := product.CommodityKey("banana prata", "KG")

	type logRow struct {
		Line           int
		IdentityStatus string
		ProductID      int64
		ProductKey     string
	}
	rows, err := st.Pool().Query(ctx,
		`SELECT pl.line, pl.identity_status, pl.product_id, p.product_key
		   FROM price_logs pl
		   JOIN products p ON p.id = pl.product_id
		  ORDER BY pl.line`)
	if err != nil {
		t.Fatalf("query logs: %v", err)
	}
	defer rows.Close()

	var got []logRow
	for rows.Next() {
		var lr logRow
		if err := rows.Scan(&lr.Line, &lr.IdentityStatus, &lr.ProductID, &lr.ProductKey); err != nil {
			t.Fatalf("scan log: %v", err)
		}
		got = append(got, lr)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("joined logs = %d rows, want 2 (FK must link every auto-save)", len(got))
	}
	want := map[int]struct {
		status string
		key    string
	}{
		1: {"gtin_new", gtinKey},
		2: {"commodity_fingerprint", commodityKey},
	}
	for _, lr := range got {
		w, ok := want[lr.Line]
		if !ok {
			t.Errorf("unexpected line %d", lr.Line)
			continue
		}
		if lr.ProductID == 0 {
			t.Errorf("line %d product_id = 0 (the created-before-insert bug)", lr.Line)
		}
		if lr.IdentityStatus != w.status || lr.ProductKey != w.key {
			t.Errorf("line %d = status:%s key:%s, want status:%s key:%s",
				lr.Line, lr.IdentityStatus, lr.ProductKey, w.status, w.key)
		}
	}
}
