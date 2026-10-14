package middleware

import (
	"net/http"
	"path/filepath"
	"strings"
)

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

// StaticFS wraps an http.Handler to serve static files from the given root.
// It is designed for development use with the dist/web bundle.
func StaticFS(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent directory traversal
		if !strings.HasPrefix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}

		// Strip the trailing slash for index.html lookup
		path := r.URL.Path
		if strings.HasSuffix(path, "/") {
			path = strings.TrimSuffix(path, "/")
		}

		// Construct the file path relative to the root
		filePath := filepath.Join(root, path)

		// Security check: ensure the resolved path is still under the root
		absRoot, err := filepath.Abs(root)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		absFile, err := filepath.Abs(filePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		if !strings.HasPrefix(absFile, absRoot) {
			http.NotFound(w, r)
			return
		}

		// Serve the file
		fs := http.FileServer(http.Dir(root))
		fs.ServeHTTP(w, r)
	})
}
