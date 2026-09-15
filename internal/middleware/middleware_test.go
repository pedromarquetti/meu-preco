package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestPanicRecovery(t *testing.T) {
	var buf bytes.Buffer
	r := chi.NewRouter()
	r.Use(PanicRecovery(testLogger(&buf)))
	r.Get("/boom", func(w http.ResponseWriter, r *http.Request) {
		panic("kaboom")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Error.Code != "internal_error" {
		t.Errorf("code = %q", body.Error.Code)
	}
	if !strings.Contains(buf.String(), "kaboom") {
		t.Errorf("panic not logged: %s", buf.String())
	}
}

func TestRequestID(t *testing.T) {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		id := RequestIDFrom(r.Context())
		if id == "" {
			t.Error("RequestIDFrom returned empty")
		}
		w.Write([]byte(id))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "client-supplied-id")
	r.ServeHTTP(rec, req)

	if rec.Body.String() != "client-supplied-id" {
		t.Errorf("incoming X-Request-ID not preserved: %q", rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") != "client-supplied-id" {
		t.Errorf("response header = %q", rec.Header().Get("X-Request-ID"))
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if id := rec.Body.String(); len(id) < 8 {
		t.Errorf("generated id too short: %q", id)
	}
}

func TestLogging(t *testing.T) {
	var buf bytes.Buffer
	r := chi.NewRouter()
	r.Use(Logging(testLogger(&buf)))
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("x"))
	})

	before := Snapshot()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))

	logs := buf.String()
	for _, want := range []string{"method=GET", "path=/ok", "status=201"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log missing %q:\n%s", want, logs)
		}
	}
	after := Snapshot()
	if after.TotalRequests != before.TotalRequests+1 {
		t.Errorf("request counter = %d, want %d", after.TotalRequests, before.TotalRequests+1)
	}
}

func TestCORSAllowedOrigin(t *testing.T) {
	r := chi.NewRouter()
	r.Use(CORS([]string{"https://app.example.com"}))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://app.example.com")
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("ACAO = %q", got)
	}
}

func TestCORSDisallowedOrigin(t *testing.T) {
	r := chi.NewRouter()
	r.Use(CORS([]string{"https://app.example.com"}))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("ACAO = %q, want empty", got)
	}
}

func TestCORSPreflight(t *testing.T) {
	r := chi.NewRouter()
	r.Use(CORS([]string{"*"}))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/healthz", nil)
	req.Header.Set("Origin", "https://whatever.example.com")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("ACAO = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Idempotency-Key") {
		t.Errorf("Allow-Headers = %q", got)
	}
}
