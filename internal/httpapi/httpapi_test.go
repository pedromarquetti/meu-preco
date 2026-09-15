package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"meupreco/internal/config"
)

var stubGroups = []string{"/api/v1/auth", "/api/v1/price-logs", "/api/v1/products", "/api/v1/admin"}

func testRouter() http.Handler {
	return New(config.Config{CORSOrigins: []string{"https://app.example.com"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func TestNfceGroupNotImplemented(t *testing.T) {
	for _, path := range []string{"/api/v1/nfce", "/api/v1/nfce/login", "/api/v1/nfce/x/y", "/api/v1/nfce/challenge/abc"} {
		rec := httptest.NewRecorder()
		testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("POST %s: status = %d, want 501", path, rec.Code)
		}
	}
}

func TestNfceSubmitUsesOnlyPost(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nfce/submit", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/v1/nfce/submit: status = %d, want 405", rec.Code)
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
