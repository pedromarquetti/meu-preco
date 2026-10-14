package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"meupreco/internal/httpx"
)

// staticFiles is generated from api/openapi.yaml and web/docs. It is embedded
// so the server does not depend on the repository or working directory.
//
//go:embed static
var staticFiles embed.FS

func documentationHandler() (http.Handler, error) {
	files, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}

	fileServer := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		if _, err := fs.Stat(files, path); err != nil {
			httpx.NotFound(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	}), nil
}

func documentationRoutes(router interface {
	Get(string, http.HandlerFunc)
	Mount(string, http.Handler)
}) error {
	handler, err := documentationHandler()
	if err != nil {
		return err
	}

	router.Get("/", func(writer http.ResponseWriter, req *http.Request) {
		http.Redirect(writer, req, "/docs/", http.StatusFound)
	})
	router.Mount("/docs/", http.StripPrefix("/docs/", handler))
	router.Get("/openapi.yaml", handler.ServeHTTP)
	router.Get("/openapi.json", handler.ServeHTTP)
	return nil
}
