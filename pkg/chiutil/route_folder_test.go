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
