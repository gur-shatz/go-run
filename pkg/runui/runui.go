package runui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
)

//go:embed static/*
var staticFiles embed.FS

// Routes returns a chi.Router that serves the embedded dashboard UI.
func Routes() chi.Router {
	r := chi.NewRouter()

	sub, _ := fs.Sub(staticFiles, "static")
	fileServer := http.FileServer(http.FS(sub))

	r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
		// Serve index.html for the root path.
		// Use ServeContent directly to avoid FileServer's redirect from /index.html → /
		if req.URL.Path == "/" || req.URL.Path == "/index.html" {
			f, err := sub.Open("index.html")
			if err != nil {
				http.NotFound(w, req)
				return
			}
			defer f.Close()
			stat, _ := f.Stat()
			http.ServeContent(w, req, "index.html", stat.ModTime(), f.(io.ReadSeeker))
			return
		}
		fileServer.ServeHTTP(w, req)
	})

	return r
}
