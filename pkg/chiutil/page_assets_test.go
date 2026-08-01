package chiutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestPageAssetsServedAndUnlisted pins the shared-asset contract: every
// folder serves bo.css / htmx.min.js / bo.js as sibling routes, so pages
// reference them relatively at any mount depth — and none of them appear
// in the folder's index.
func TestPageAssetsServedAndUnlisted(t *testing.T) {
	r := chi.NewRouter()
	folder := NewRouteFolder(r, "/bo")
	folder.GetDesc("/page", "a page", func(w http.ResponseWriter, _ *http.Request) {})

	for _, path := range []string{"/bo/bo.css", "/bo/htmx.min.js", "/bo/bo.js"} {
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, path, nil))
		if resp.Code != http.StatusOK || resp.Body.Len() == 0 {
			t.Fatalf("asset %s = %d (%d bytes), want 200 with content", path, resp.Code, resp.Body.Len())
		}
	}

	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/bo/index.json", nil))
	var index FolderIndex
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	for _, e := range index.Entries {
		if isPageAssetPath(e.Path) || strings.Contains(e.Path, "bo.css") {
			t.Fatalf("asset leaked into index: %+v", e)
		}
	}
}
