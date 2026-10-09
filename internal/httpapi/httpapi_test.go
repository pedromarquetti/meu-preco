package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"meupreco/internal/config"
	"meupreco/internal/domain"
)

// stubGroups are the endpoint groups that are still full placeholders; both
// answer 501 for any method until their owning phase lands.
var stubGroups = []string{"/api/v1/products"}

// fakeStores is an in-memory domain.Stores for httptest router creation. The
// repositories it does not exercise are the embedded nil interface (they
// panic if called); auth tests swap real in-memory fakes in via the fields.
type fakeStores struct {
	domain.Stores
	users         domain.UserStore
	refreshTokens domain.RefreshTokenStore
	pingErr       error
}

func (f *fakeStores) Users() domain.UserStore                 { return f.users }
func (f *fakeStores) RefreshTokens() domain.RefreshTokenStore { return f.refreshTokens }
func (f *fakeStores) Ping(context.Context) error              { return f.pingErr }

func testRouter() http.Handler {
	return New(config.Config{CORSOrigins: []string{"https://app.example.com"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		&fakeStores{})
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
}

func TestReadyz(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestNotFoundEnvelope(t *testing.T) {
	for _, path := range []string{"/nope", "/price-logs-extra/x", "/nested/deep/y"} {
		rec := httptest.NewRecorder()
		testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
			continue
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: unmarshal: %v", path, err)
			continue
		}
		if body.Error.Code != "not_found" {
			t.Errorf("%s: code = %q", path, body.Error.Code)
		}
	}
}

func TestStubGroups(t *testing.T) {
	for _, prefix := range stubGroups {
		for _, path := range []string{prefix, prefix + "/login", prefix + "/x/y"} {
			rec := httptest.NewRecorder()
			testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
			if rec.Code != http.StatusNotImplemented {
				t.Errorf("%s: status = %d, want 501", path, rec.Code)
				continue
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Errorf("%s: unmarshal: %v", path, err)
				continue
			}
			if body.Error.Code != "not_implemented" {
				t.Errorf("%s: code = %q", path, body.Error.Code)
			}
		}
	}
}

// TestNfceRequireAuth asserts the two-phase ingestion surface is gated:
// every route under /nfce answers 401 without a bearer token.
func TestNfceRequireAuth(t *testing.T) {
	for _, path := range []string{
		"/api/v1/nfce/new",
		"/api/v1/nfce/submit",
		"/api/v1/nfce/x/y",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rec := httptest.NewRecorder()
			testRouter().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: status = %d, want 401", method, path, rec.Code)
			}
		}
	}
}

// TestNfceGroupNotImplemented asserts auth-gated unknown paths under /nfce
// still answer 501 once the token passes.
func TestNfceGroupNotImplemented(t *testing.T) {
	for _, path := range []string{"/api/v1/nfce", "/api/v1/nfce/login", "/api/v1/nfce/challenge/abc"} {
		rec := httptest.NewRecorder()
		testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s: status = %d, want 401 (auth gate precedes route resolution)", path, rec.Code)
		}
	}
}

func TestNfceSubmitUsesOnlyPost(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nfce/submit", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/nfce/submit: status = %d, want 401 (gate before method check)", rec.Code)
	}
}

func TestCORSHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("ACAO = %q", got)
	}
}
