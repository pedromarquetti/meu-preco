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

	before := Report()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))

	logs := buf.String()
	for _, want := range []string{"method=GET", "path=/ok", "status=201"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log missing %q:\n%s", want, logs)
		}
	}
	after := Report()
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

func TestSecurityHeaders(t *testing.T) {
	r := chi.NewRouter()
	r.Use(SecurityHeaders)
	r.Get("/", okHandler)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func TestMetricsReport(t *testing.T) {
	r := chi.NewRouter()
	r.Use(Logging(testLogger(&bytes.Buffer{})))
	r.Get("/ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	r.Get("/fail", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })

	before := Report()
	for i := 0; i < 3; i++ {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/fail", nil))

	report := Report()
	if got := report.TotalRequests - before.TotalRequests; got != 4 {
		t.Errorf("TotalRequests delta = %d, want 4", got)
	}
	if got := report.AuthFailures - before.AuthFailures; got != 1 {
		t.Errorf("AuthFailures delta = %d, want 1", got)
	}
	for _, class := range []string{"2xx", "4xx"} {
		if report.ByStatus[class] < before.ByStatus[class] {
			t.Errorf("ByStatus[%s] went backwards", class)
		}
	}
	if report.ByStatus["4xx"]-before.ByStatus["4xx"] != 1 {
		t.Errorf("4xx delta = %d, want 1", report.ByStatus["4xx"]-before.ByStatus["4xx"])
	}

	var got *RouteMetrics
	for i := range report.Routes {
		rm := &report.Routes[i]
		if rm.Pattern == "/ok" {
			got = rm
		}
	}
	if got == nil {
		t.Fatal("route /ok not in report")
	}
	// The /ok route accumulates across the whole package; assert the delta.
	var beforeOK *RouteMetrics
	for i := range before.Routes {
		rm := &before.Routes[i]
		if rm.Pattern == "/ok" {
			rm2 := *rm
			beforeOK = &rm2
		}
	}
	deltaCount := got.Count
	if beforeOK != nil {
		deltaCount -= beforeOK.Count
	}
	if deltaCount != 3 {
		t.Errorf("/ok count delta = %d, want 3", deltaCount)
	}
	if n := len(got.Histogram); n != len(latencyBucketsMs)+1 {
		t.Errorf("histogram bins = %d, want %d", n, len(latencyBucketsMs)+1)
	}
	var histTotal int64
	for _, c := range got.Histogram {
		histTotal += c
	}
	if histTotal != got.Count {
		t.Errorf("histogram total %d != route count %d", histTotal, got.Count)
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
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Content-Type") || strings.Contains(got, "Idempotency-Key") {
		t.Errorf("Allow-Headers = %q", got)
	}
}
