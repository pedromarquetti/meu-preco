package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"meupreco/internal/config"
	"meupreco/internal/domain"
	"meupreco/internal/testdb"
)

// TestAdminCreateSynonym covers POST /api/v1/admin/synonym: validation, the
// normalized persistence contract, conflict handling and role gating.
func TestAdminCreateSynonym(t *testing.T) {
	st := integrationStore(t)
	if err := testdb.Truncate(context.Background(), st.Pool()); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()

	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM commodity_synonyms WHERE synonym IN ('qjx', 'mussarela')`); err != nil {
		t.Fatalf("clean test synonyms: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(),
			`DELETE FROM commodity_synonyms WHERE synonym IN ('qjx', 'mussarela')`)
	})

	admin := &domain.User{Name: "Root", Email: "root@example.com", PasswordHash: "h", Role: "admin"}
	if err := st.Users().Create(ctx, admin); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	api := roleRouter(t, st, pgResolver{}, admin.ID, "admin")

	// 1. Create an abbreviation prefix; the synonym and unit are normalized.
	rec, out := do(t, api, http.MethodPost, "/api/v1/admin/synonym",
		`{"synonym":" QJX ","canonical_name":"Queijo Mussarela","category":"laticinios","unit":"kg","prefix_expansion":"queijo"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create synonym = %d (%s)", rec.Code, rec.Body.String())
	}
	if out["synonym"] != "qjx" || out["canonical_name"] != "queijo mussarela" || out["unit"] != "KG" {
		t.Errorf("created synonym = %+v", out)
	}
	if out["source"] != "admin" || out["prefix_expansion"] != "queijo" {
		t.Errorf("created source/prefix = %+v", out)
	}
	if jint(out["id"]) == 0 {
		t.Errorf("created synonym has no id: %+v", out)
	}

	row, err := st.CommoditySynonyms().Canonicalize(ctx, "qjx")
	if err != nil {
		t.Fatalf("persisted synonym: %v", err)
	}
	if row.CanonicalName != "queijo mussarela" || row.PrefixExpansion == nil || *row.PrefixExpansion != "queijo" {
		t.Errorf("persisted row = %+v", row)
	}

	// 2. A plain exact synonym is accepted too.
	rec, out = do(t, api, http.MethodPost, "/api/v1/admin/synonym",
		`{"synonym":"mussarela","canonical_name":"queijo mussarela","unit":"KG"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create exact synonym = %d (%s)", rec.Code, rec.Body.String())
	}
	if out["prefix_expansion"] != nil {
		t.Errorf("exact synonym should have no prefix expansion: %+v", out)
	}

	// 3. Malformed JSON is a clean 400 invalid_request.
	rec, out = do(t, api, http.MethodPost, "/api/v1/admin/synonym", `{"synonym":`)
	if rec.Code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_request" {
		t.Errorf("malformed body = %d (%s)", rec.Code, rec.Body.String())
	}

	// 4. Empty/mistyped fields are 400 invalid_assignment.
	for _, body := range []string{
		`{"synonym":"qjo","canonical_name":""}`,
		`{"synonym":"","canonical_name":"queijo prato"}`,
		`{"synonym":"qj brie","canonical_name":"queijo brie","unit":"liters"}`,
	} {
		rec, out = do(t, api, http.MethodPost, "/api/v1/admin/synonym", body)
		if rec.Code != http.StatusBadRequest || out["error"].(map[string]any)["code"] != "invalid_assignment" {
			t.Errorf("invalid body %s = %d (%s)", body, rec.Code, rec.Body.String())
		}
	}

	// 5. Re-creating the same synonym is a conflict.
	rec, out = do(t, api, http.MethodPost, "/api/v1/admin/synonym",
		`{"synonym":"qjx","canonical_name":"queijo prato"}`)
	if rec.Code != http.StatusConflict || out["error"].(map[string]any)["code"] != "conflict" {
		t.Errorf("duplicate = %d (%s)", rec.Code, rec.Body.String())
	}

	// 6. Non-admin callers are forbidden, anonymous callers are unauthorized.
	unpriv := roleRouter(t, st, pgResolver{}, admin.ID, "user")
	rec, _ = do(t, unpriv, http.MethodPost, "/api/v1/admin/synonym",
		`{"synonym":"qjo","canonical_name":"queijo mussarela"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin create = %d (%s), want 403", rec.Code, rec.Body.String())
	}

	raw := newRouter(config.Config{JWTSecret: testJWTSecret, CORSOrigins: []string{"https://app.example.com"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)), pgResolver{}, st, nil)
	rec, _ = do(t, raw, http.MethodPost, "/api/v1/admin/synonym",
		`{"synonym":"qjo","canonical_name":"queijo mussarela"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous create = %d (%s), want 401", rec.Code, rec.Body.String())
	}
}
