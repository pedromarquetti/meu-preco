package httpx

import (
	"encoding/json"
	"net/http"
)

// ErrorBody is the canonical error envelope used by every handler.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error writes the canonical error envelope: {"error":{"code","message"}}.
func Error(w http.ResponseWriter, _ *http.Request, status int, code, message string) {
	writeJSON(w, status, map[string]ErrorBody{"error": {Code: code, Message: message}})
}

// Respond writes a success JSON body.
func Respond(w http.ResponseWriter, status int, v any) {
	writeJSON(w, status, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// Receipt drafts, plans and auth bodies are private by nature; no-store
	// keeps caches and proxies from replaying them.
	w.Header().Set("Cache-Control", "no-store")

	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// NotFound reports an unmatched route with the canonical envelope.
func NotFound(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusNotFound, "not_found", "route not found")
}

// MethodNotAllowed reports an existing path with a banned method.
func MethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

// NotImplemented is the placeholder for endpoint groups mounted before their
// owning phase lands.
func NotImplemented(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusNotImplemented, "not_implemented", "endpoint not implemented yet")
}

// InternalError reports an unexpected server failure without leaking details.
func InternalError(w http.ResponseWriter, r *http.Request) {
	Error(w, r, http.StatusInternalServerError, "internal_error", "internal error")
}
