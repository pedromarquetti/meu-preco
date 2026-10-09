// Package middleware provides the shared HTTP middleware chain: request IDs,
// panic recovery, structured access logging, CORS, and basic request metrics.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"meupreco/internal/httpx"
)

type ctxKey int

const requestIDKey ctxKey = 0

// RequestID injects a X-Request-ID (incoming if provided, otherwise random)
// and makes it available in the request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			var b [8]byte
			if _, err := rand.Read(b[:]); err == nil {
				id = hex.EncodeToString(b[:])
			}
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// RequestIDFrom returns the request ID associated with the context.
func RequestIDFrom(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// PanicRecovery converts a handler panic into a 500 error envelope and logs
// the panic with a stack trace, without crashing the process.
func PanicRecovery(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic recovered",
						"panic", fmt.Sprint(rec),
						"stack", string(debug.Stack()),
						"method", r.Method,
						"path", r.URL.Path,
						"request_id", RequestIDFrom(r.Context()),
					)
					httpx.InternalError(w, r)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Logging emits one structured access log line per request with method, path,
// status, latency and byte count. It also feeds the package-level request
// metrics (see Metrics).
func Logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sr := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(sr, r)

			if sr.status == 0 {
				sr.status = http.StatusOK
			}

			dur := time.Since(start)
			recordRequest(sr.status, dur)

			log.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sr.status,
				"duration_ms", dur.Milliseconds(),
				"bytes", sr.bytes,
				"request_id", RequestIDFrom(r.Context()),
				"remote_addr", r.RemoteAddr,
			)
		})
	}
}

// CORS permits browser clients from the configured origins. The special value
// "*" reflects any origin with Access-Control-Allow-Origin: *. Preflight
// OPTIONS requests are answered without reaching the handler.
func CORS(origins []string) func(http.Handler) http.Handler {
	const (
		allowMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
		allowHeaders = "Authorization, Content-Type, X-Request-ID"
	)

	exact := make(map[string]bool, len(origins))
	wildcard := false

	for _, o := range origins {
		if o == "*" {
			wildcard = true
		} else {
			exact[strings.ToLower(o)] = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if wildcard {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				} else if exact[strings.ToLower(origin)] {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
				}
			}
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", allowMethods)
				w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}
