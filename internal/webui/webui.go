// Package webui embeds the built SPA (web/dist). The build copies web/dist
// into internal/webui/dist before compiling, so the frontend is part of the
// enclave image and therefore covered by PCR0.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the SPA. Unknown paths without a file extension fall back to
// index.html so client-side routing works.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
			p = "index.html"
		}
		if p == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
			if _, err := fs.Stat(sub, p); err != nil {
				http.Error(w, "web UI not built into this binary", http.StatusNotFound)
				return
			}
			http.ServeFileFS(w, r, sub, p)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
