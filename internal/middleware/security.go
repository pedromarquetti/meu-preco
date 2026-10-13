package middleware

import "net/http"

// SecurityHeaders stamps every response with conservative browser-facing
// hardening headers:
//
//	X-Content-Type-Options: nosniff  — browsers honor the declared Content-Type
//	X-Frame-Options:        DENY     — no framing of API responses
//	Referrer-Policy:        no-referrer — receipts are private; never leak referer
//
// Cache-Control: no-store is layered on JSON payloads at the write boundary
// (httpx.writeJSON) so every payload, previews and auth bodies included, is
// never cached by intermediaries.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
