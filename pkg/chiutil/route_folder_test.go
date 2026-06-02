package chiutil_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gur-shatz/go-run/pkg/chiutil"

	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RouteFolder", func() {
	It("registers GET handlers", func() {
		router := chi.NewRouter()
		folder := chiutil.NewRouteFolder(router, "/backoffice")

		folder.GetHandlerDesc("/status", "Status page", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))

		req := httptest.NewRequest(http.MethodGet, "/backoffice/status", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(w.Body.String()).To(Equal("ok"))

		req = httptest.NewRequest(http.MethodGet, "/backoffice/index.json", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))

		var index chiutil.FolderIndex
		Expect(json.Unmarshal(w.Body.Bytes(), &index)).To(Succeed())
		Expect(index.Entries).To(HaveLen(1))
		Expect(index.Entries[0].Name).To(Equal("status"))
		Expect(index.Entries[0].Description).To(Equal("Status page"))
	})

	It("redirects a bare folder URL to its trailing-slash form", func() {
		router := chi.NewRouter()
		bo := chiutil.NewRouteFolder(router, "/backoffice")
		bo.WildcardFolder("components", "name", func(r chi.Router) {
			r.Get("/info", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("info")) })
		}).Add("alpha", "Alpha")

		// The index page resolves index.json, entry links, and the breadcrumb
		// relative to its own URL, so the bare form must redirect. The target
		// is relative (final segment) so it survives a path-stripping proxy.
		cases := []struct{ path, wantLocation string }{
			{"/backoffice", "backoffice/"},
			{"/backoffice/components", "components/"},
			{"/backoffice/components/alpha", "alpha/"},
			{"/backoffice?x=1", "backoffice/?x=1"},
		}
		for _, c := range cases {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			Expect(w.Code).To(Equal(http.StatusMovedPermanently), "for %s", c.path)
			Expect(w.Header().Get("Location")).To(Equal(c.wantLocation), "for %s", c.path)
		}

		// The trailing-slash form serves the index directly.
		req := httptest.NewRequest(http.MethodGet, "/backoffice/", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
	})

	It("reports index paths relative to the mount root so the breadcrumb's home is the mount root", func() {
		router := chi.NewRouter()
		bo := chiutil.NewRouteFolder(router, "/backoffice")
		bo.Folder("logs")
		bo.WildcardFolder("components", "name", func(r chi.Router) {
			r.Get("/info", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("info")) })
		}).Add("alpha", "Alpha")

		pathOf := func(url string) string {
			req := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			Expect(w.Code).To(Equal(http.StatusOK), "for %s", url)
			var index chiutil.FolderIndex
			Expect(json.Unmarshal(w.Body.Bytes(), &index)).To(Succeed())
			return index.Path
		}

		// The mount root reports "/" (it is home), and every descendant reports
		// its path relative to home — never the absolute /backoffice/... — so the
		// SPA folds /backoffice into the breadcrumb's home rather than showing it
		// as a navigable crumb.
		Expect(pathOf("/backoffice/index.json")).To(Equal("/"))
		Expect(pathOf("/backoffice/logs/index.json")).To(Equal("/logs"))
		Expect(pathOf("/backoffice/components/index.json")).To(Equal("/components"))
		Expect(pathOf("/backoffice/components/alpha/index.json")).To(Equal("/components/alpha"))
	})

	It("shows a sub-folder's description on the parent index", func() {
		router := chi.NewRouter()
		parent := chiutil.NewRouteFolder(router, "/backoffice")
		parent.Folder("accounts").Description("Customer accounts")

		req := httptest.NewRequest(http.MethodGet, "/backoffice/index.json", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))

		var index chiutil.FolderIndex
		Expect(json.Unmarshal(w.Body.Bytes(), &index)).To(Succeed())
		Expect(index.Entries).To(HaveLen(1))
		Expect(index.Entries[0].Name).To(Equal("accounts"))
		Expect(index.Entries[0].IsFolder).To(BeTrue())
		Expect(index.Entries[0].Description).To(Equal("Customer accounts"))
	})

	It("isolates HTML previews in an iframe", func() {
		router := chi.NewRouter()
		chiutil.NewRouteFolder(router, "/backoffice")

		req := httptest.NewRequest(http.MethodGet, "/backoffice/", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))

		body := w.Body.String()
		Expect(body).To(ContainSubstring("viewerMode === 'iframe' || contentType.includes('text/html')"))
		Expect(strings.Contains(body, "innerHTML = text")).To(BeFalse())
	})

	It("persists selected endpoint previews in the URL hash", func() {
		router := chi.NewRouter()
		chiutil.NewRouteFolder(router, "/backoffice")

		req := httptest.NewRequest(http.MethodGet, "/backoffice/", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))

		body := w.Body.String()
		Expect(body).To(ContainSubstring("function endpointHash(path, method)"))
		Expect(body).To(ContainSubstring("params.set('method', method)"))
		Expect(body).To(ContainSubstring("params.set('path', path)"))
		Expect(body).To(ContainSubstring("restoreSelectionFromHash();"))
		Expect(body).To(ContainSubstring("history.pushState(null, '', hash)"))
	})
})
