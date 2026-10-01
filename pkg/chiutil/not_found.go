package chiutil

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// folderKey is the request-context key of the innermost folder serving the
// request.
type folderKey struct{}

// initRouter wires a new folder router: misses get the navigable 404, and
// handlers below can find their folder (see NotFound). It must run before
// any route is registered, since chi requires middleware first.
func (this *RouteFolder) initRouter() {
	this.router.NotFound(this.serveNotFound)
	this.router.Use(this.enter)
}

func (this *RouteFolder) enter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), folderKey{}, this)))
	})
}

// NotFound answers a not-found from inside a handler served by a folder
// tree (an ObjectRoute, a WildcardFolder route, a route on a Scope): a
// browser navigation gets the 404 page that leads back to the handler's
// folder and its ancestors, everything else the plain http.NotFound body.
// Outside a folder tree it is http.NotFound.
func NotFound(w http.ResponseWriter, r *http.Request) {
	folder, ok := r.Context().Value(folderKey{}).(*RouteFolder)
	if !ok {
		http.NotFound(w, r)
		return
	}
	folder.serveNotFound(w, r)
}

// serveNotFound answers a miss below this folder. A browser navigation gets
// an HTML page whose links climb back to this folder and its ancestors;
// everything else keeps the plain http.NotFound body.
func (this *RouteFolder) serveNotFound(w http.ResponseWriter, r *http.Request) {
	missing := strings.Trim(this.remainder(r), "/")
	this.writeNotFound(w, r, fmt.Sprintf("%q not found in %s", missing, this.displayName(r)))
}

// notFoundItem is serveNotFound for an unknown item of the collection this
// folder lists: the message names the collection and the id.
func (this *RouteFolder) notFoundItem(w http.ResponseWriter, r *http.Request, id string) {
	this.writeNotFound(w, r, fmt.Sprintf("%s has no item %q", this.displayName(r), id))
}

// wantsNotFoundPage reports whether the request is a browser navigation.
// Shell previews are excluded: the viewer renders them in an iframe whose
// relative links would resolve against the shell, not the missed path.
func wantsNotFoundPage(r *http.Request) bool {
	if r.URL.Query().Get("preview") == "true" {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// remainder returns the request path below this folder, with a leading
// slash. It is taken from the full request path rather than chi's RoutePath
// because a handler inherited by a sub-mux sees that sub-mux's RoutePath.
func (this *RouteFolder) remainder(r *http.Request) string {
	base, _ := this.expandedPaths(r)
	if base == "/" {
		return r.URL.Path
	}
	if strings.HasPrefix(r.URL.Path, base) {
		return strings.TrimPrefix(r.URL.Path, base)
	}
	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePath != "" {
		return rctx.RoutePath
	}
	return r.URL.Path
}

// displayName names this folder in the 404 message: its title, else its last
// path segment, else "home".
func (this *RouteFolder) displayName(r *http.Request) string {
	if this.title != "" {
		return this.title
	}
	segs := pathSegments(this.relPath(r))
	if len(segs) == 0 {
		return "home"
	}
	return segs[len(segs)-1]
}

// writeNotFound writes the 404. Every link is relative (a count of "../"
// from the missed path), so the page stays correct behind a path-stripping
// proxy, the same rule redirectToTrailingSlash follows.
func (this *RouteFolder) writeNotFound(w http.ResponseWriter, r *http.Request, message string) {
	if !wantsNotFoundPage(r) {
		http.NotFound(w, r)
		return
	}

	folderSegs := pathSegments(this.relPath(r))
	missSegs := pathSegments(this.remainder(r))

	// The browser resolves relative links against the missed path's
	// directory: the path itself with a trailing slash, its parent without.
	up := len(missSegs)
	if !strings.HasSuffix(r.URL.Path, "/") {
		up--
	}
	if up < 0 {
		up = 0
	}
	climb := func(n int) string {
		if n == 0 {
			return "./"
		}
		return strings.Repeat("../", n)
	}

	var crumbs strings.Builder
	fmt.Fprintf(&crumbs, `<a href="%s">home</a>`, climb(up+len(folderSegs)))
	for i, seg := range folderSegs {
		fmt.Fprintf(&crumbs, ` / <a href="%s">%s</a>`, climb(up+len(folderSegs)-1-i), html.EscapeString(seg))
	}
	for _, seg := range missSegs {
		fmt.Fprintf(&crumbs, ` / <span class="miss">%s</span>`, html.EscapeString(seg))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><title>Not found</title><style>
body{font:14px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#e6edf3;margin:0;padding:24px;background:#161b22}
h1{font-size:18px;margin:0 0 6px;color:#e6edf3}
p{color:#8b949e;margin:0 0 16px}
nav{font:13px ui-monospace,SFMono-Regular,Menlo,monospace;color:#8b949e;margin:0 0 20px}
a{color:#58a6ff;text-decoration:none}
a:hover{text-decoration:underline}
.miss{color:#f85149}
.back{display:inline-block;padding:6px 12px;border:1px solid #30363d;border-radius:6px;background:#0d1117}
</style></head><body>`)
	fmt.Fprint(w, `<h1>404 Not found</h1>`)
	fmt.Fprintf(w, `<p>%s</p>`, html.EscapeString(message))
	fmt.Fprintf(w, `<nav>%s</nav>`, crumbs.String())
	fmt.Fprintf(w, `<a class="back" href="%s">&larr; Back to %s</a>`, climb(up), html.EscapeString(this.displayName(r)))
	fmt.Fprint(w, `</body></html>`)
}

// pathSegments splits p on "/" and drops empty segments.
func pathSegments(p string) []string {
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}
