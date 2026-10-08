package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"meupreco/internal/config"
	"meupreco/internal/domain"
)

func newTestRouter(s domain.Stores) http.Handler {
	return New(config.Config{CORSOrigins: nil},
		slog.New(slog.NewTextHandler(io.Discard, nil)), s)
}

func TestReadyzOK(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter(&fakeStores{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestReadyzDBUnreachableIs503(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter(&fakeStores{pingErr: errors.New("connection refused")}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestReadyzNoDBConfiguredIs503(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
