package server

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/dickymuliafiqri/firefly/frontend"
)

// registerFrontendRoutes mounts the embedded Single Page Application (SPA)
// dashboard and its static assets onto the ServeMux.
func (deps RouterDeps) registerFrontendRoutes(mux *http.ServeMux) {
	distFS := frontend.FS()

	// Handler to serve index.html (embedded first, disk candidate fallback)
	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		// 1. Try embedded dist/index.html
		if distFS != nil {
			f, err := distFS.Open("index.html")
			if err == nil {
				defer f.Close()
				data, readErr := io.ReadAll(f)
				if readErr == nil && len(data) > 0 {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.Header().Set("Cache-Control", "no-cache")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(data)
					return
				}
			}
		}

		// 2. Fallback to candidate file paths on disk (development mode)
		candidates := []string{
			"frontend/dist/index.html",
			"../frontend/dist/index.html",
			"../../frontend/dist/index.html",
			"dist/index.html",
		}
		for _, p := range candidates {
			if b, err := os.ReadFile(p); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(b)
				return
			}
		}

		// 3. Fallback to plain ok if no dashboard file found
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}

	// Root route: GET /{$}
	mux.HandleFunc("GET /{$}", serveIndex)

	// SPA tab routes so direct navigation and browser refresh works on tabs
	tabs := []string{
		"overview",
		"upstreams",
		"models",
		"tenants",
		"telemetry",
		"settings",
		"playground",
	}
	for _, tab := range tabs {
		mux.HandleFunc("GET /"+tab, serveIndex)
	}

	// Assets route: GET /assets/
	if distFS != nil {
		assetsSub, err := fs.Sub(distFS, "assets")
		if err == nil {
			fileServer := http.FileServer(http.FS(assetsSub))
			mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Long-term immutable caching for Vite hashed asset chunks
				if strings.Contains(r.URL.Path, "-") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
			})))
		}
	}
}
