package chiutil

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Shared backoffice page assets. Every folder serves them as hidden
// sibling routes, so any page in the tree references them with a plain
// relative URL ("bo.css", "alpine.min.js") regardless of where the tree is
// mounted or which proxy prefix the request arrived under — the browser
// resolves the prefix, not the page. Registered at both the folder level
// and the objects/wildcard item level so pages at either depth find them
// as siblings.

//go:embed bo.css
var pageAssetCSS []byte

// pageAssetAlpine is the vendored Alpine.js 3.15.12 minified CDN
// distribution (https://unpkg.com/alpinejs@3/dist/cdn.min.js), embedded
// so backoffice pages need no network egress to bind themselves.
//
// It replaced a vendored htmx here. The pages that would have used htmx
// render from a JSON document instead — the server decides how a value
// reads and the page binds the result — which leaves no fragment swaps
// for htmx to do. Bring it back if a page ever wants server-rendered
// HTML fragments again; nothing about Alpine precludes that.
//
//go:embed alpine.min.js
var pageAssetAlpine []byte

//go:embed bo.js
var pageAssetJS []byte

// registerPageAssets mounts the shared assets on a router. Plain router
// registrations, not index entries — the routes are callable but never
// listed.
func registerPageAssets(r chi.Router) {
	r.Get("/bo.css", servePageAsset("text/css; charset=utf-8", pageAssetCSS))
	r.Get("/alpine.min.js", servePageAsset("text/javascript; charset=utf-8", pageAssetAlpine))
	r.Get("/bo.js", servePageAsset("text/javascript; charset=utf-8", pageAssetJS))
}

// isPageAssetPath reports whether a route path is one of the shared page
// assets — the walk-based indexers use it to keep them out of listings.
func isPageAssetPath(route string) bool {
	switch strings.TrimPrefix(route, "/") {
	case "bo.css", "alpine.min.js", "bo.js":
		return true
	}
	return false
}

func servePageAsset(contentType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		// Static per binary; short max-age keeps upgrades visible within
		// minutes while sparing the repeated transfer during a session.
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = w.Write(body)
	}
}
