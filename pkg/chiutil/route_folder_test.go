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

		indexOf := func(url string) chiutil.FolderIndex {
			req := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			Expect(w.Code).To(Equal(http.StatusOK), "for %s", url)
			var index chiutil.FolderIndex
			Expect(json.Unmarshal(w.Body.Bytes(), &index)).To(Succeed())
			return index
		}
		pathOf := func(url string) string { return indexOf(url).Path }

		// A wildcard instance page is titled by its id (and carries the
		// registered description), not by the listing folder's "Components".
		alpha := indexOf("/backoffice/components/alpha/index.json")
		Expect(alpha.Title).To(Equal("alpha"))
		Expect(alpha.Description).To(Equal("Alpha"))

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

	It("serves an index handler as the default preview without replacing the folder shell", func() {
		router := chi.NewRouter()
		folder := chiutil.NewRouteFolder(router, "/backoffice")
		folder.Index(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("overview"))
		})

		req := httptest.NewRequest(http.MethodGet, "/backoffice/index.json", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))

		var index chiutil.FolderIndex
		Expect(json.Unmarshal(w.Body.Bytes(), &index)).To(Succeed())
		Expect(index.HasIndex).To(BeTrue())
		Expect(index.Entries).To(BeEmpty())

		req = httptest.NewRequest(http.MethodGet, "/backoffice/?preview=true", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(w.Body.String()).To(Equal("overview"))

		req = httptest.NewRequest(http.MethodGet, "/backoffice/", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(w.Body.String()).To(ContainSubstring("Select a GET endpoint to view response"))
	})

	It("serves a default index preview table from the folder index entries", func() {
		router := chi.NewRouter()
		folder := chiutil.NewRouteFolder(router, "/backoffice").
			Title("Backoffice").
			Description("Operational routes")
		folder.GetDesc("/status", "Status page", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
		folder.PostFunc(chiutil.PostArgs{
			Path:        "/flush",
			Description: "Flush cache",
			Handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("flushed"))
			},
		})
		folder.Folder("accounts").Description("Customer accounts")

		req := httptest.NewRequest(http.MethodGet, "/backoffice/index.json", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))

		var index chiutil.FolderIndex
		Expect(json.Unmarshal(w.Body.Bytes(), &index)).To(Succeed())
		Expect(index.HasIndex).To(BeTrue())

		req = httptest.NewRequest(http.MethodGet, "/backoffice/?preview=true", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))

		body := w.Body.String()
		Expect(body).To(ContainSubstring("Backoffice"))
		Expect(body).To(ContainSubstring("Operational routes"))
		Expect(body).To(ContainSubstring("background:#161b22"))
		Expect(body).To(ContainSubstring("chiutil:select"))
		Expect(body).To(ContainSubstring("status"))
		Expect(body).To(ContainSubstring("Status page"))
		Expect(body).To(ContainSubstring(`href="accounts/" target="_parent"`))
		Expect(body).To(ContainSubstring("Customer accounts"))
		Expect(body).NotTo(ContainSubstring("Flush cache"))
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
