// Package adminui embeds the built React admin console and serves it from the
// Go binary at /admin/ (the "bundled" deployment). The same built assets can
// also be served standalone by a separate static server / container.
//
// The SPA uses hash-based routing and relative asset paths, so the exact mount
// path ("/admin/" here, "/" when standalone) does not matter and no
// server-side SPA route fallback is required.
package adminui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Register mounts the admin console at /admin/ on mux. If the console was not
// built (only the placeholder is embedded), it still serves that placeholder.
func Register(mux *http.ServeMux) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(sub))

	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	mux.Handle("GET /admin/", http.StripPrefix("/admin/", serveSPA(sub, fileServer)))
}

// serveSPA serves a requested asset if it exists, otherwise falls back to
// index.html (harmless with hash routing, but robust if a real path is hit).
func serveSPA(sub fs.FS, fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := sub.Open(p); err == nil {
			_ = f.Close()
			// Long cache for fingerprinted assets; the HTML shell stays fresh.
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w, sub)
	})
}

func serveIndex(w http.ResponseWriter, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "admin console not built", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
