package chiutil_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

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
})
