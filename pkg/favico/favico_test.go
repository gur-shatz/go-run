package favico

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSVGNormalizesNameAndStatusColors(t *testing.T) {
	svg := string(SVG(Options{Name: "local", Status: StatusWarn}))
	if want := ">LO</text>"; !strings.Contains(svg, want) {
		t.Fatalf("SVG() missing %q in %s", want, svg)
	}
	if want := `fill="#facc15"`; !strings.Contains(svg, want) {
		t.Fatalf("SVG() missing warning fill %q in %s", want, svg)
	}
	if want := `textLength="52"`; !strings.Contains(svg, want) {
		t.Fatalf("SVG() missing text sizing %q in %s", want, svg)
	}
}

func TestHandlerServesDynamicNoCacheSVG(t *testing.T) {
	h := Handler("rc", func(*http.Request) Status { return StatusFail })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-cache") {
		t.Fatalf("cache-control = %q", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ">RC</text>") || !strings.Contains(body, `fill="#dc2626"`) {
		t.Fatalf("unexpected SVG body: %s", body)
	}
}
