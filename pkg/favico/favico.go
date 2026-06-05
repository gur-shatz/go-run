package favico

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// Status is the favicon-level health rollup.
type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// Options describes one generated favicon.
type Options struct {
	// Name is rendered in the icon. It is uppercased and clipped to two runes.
	Name string

	// Status controls the icon background color.
	Status Status
}

// SVG returns a 64x64 full-bleed SVG favicon.
func SVG(opts Options) []byte {
	name := normalizeName(opts.Name)
	bg, fg := colors(opts.Status)
	return []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64"><rect width="64" height="64" fill="%s"/><text x="32" y="34" text-anchor="middle" dominant-baseline="central" font-family="Arial, Helvetica, sans-serif" font-size="42" font-weight="800" textLength="52" lengthAdjust="spacingAndGlyphs" fill="%s">%s</text></svg>`, bg, fg, html.EscapeString(name)))
}

// Handler returns an HTTP handler for a dynamic SVG favicon. The status
// callback is evaluated on every request so callers can reflect live state.
func Handler(name string, status func(*http.Request) Status) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := StatusOK
		if status != nil {
			st = status(r)
		}
		Serve(w, Options{Name: name, Status: st})
	})
}

// Serve writes one SVG favicon response with headers that discourage browser
// caching. Browsers are aggressive about favicon cache, so dynamic icons need
// explicit no-cache headers.
func Serve(w http.ResponseWriter, opts Options) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	_, _ = w.Write(SVG(opts))
}

func normalizeName(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		name = "GR"
	}
	runes := []rune(name)
	if len(runes) > 2 {
		return string(runes[:2])
	}
	return name
}

func colors(status Status) (string, string) {
	switch status {
	case StatusFail:
		return "#dc2626", "#ffffff"
	case StatusWarn:
		return "#facc15", "#111827"
	default:
		return "#16a34a", "#ffffff"
	}
}
