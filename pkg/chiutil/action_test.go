package chiutil_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/gur-shatz/go-run/pkg/chiutil"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Action", func() {
	It("marks form actions for iframe rendering in the folder viewer", func() {
		req := httptest.NewRequest(http.MethodGet, "/submit", nil)
		w := httptest.NewRecorder()

		chiutil.Form("Submit", []string{"id"}).ServeHTML(w, req)

		Expect(w.Header().Get("Content-Type")).To(ContainSubstring("text/html"))
		Expect(w.Header().Get("X-Chiutil-Viewer")).To(Equal("iframe"))
		Expect(w.Body.String()).To(ContainSubstring("window.__chiutilActionPostURL || '\\/submit'"))
		Expect(w.Body.String()).To(ContainSubstring("fetch(postURL"))
		Expect(w.Body.String()).NotTo(ContainSubstring("window.location.href"))
	})

	It("marks JSON form actions for iframe rendering in the folder viewer", func() {
		req := httptest.NewRequest(http.MethodGet, "/submit", nil)
		w := httptest.NewRecorder()

		chiutil.JsonForm("Submit", `{"id":"123"}`).ServeHTML(w, req)

		Expect(w.Header().Get("Content-Type")).To(ContainSubstring("text/html"))
		Expect(w.Header().Get("X-Chiutil-Viewer")).To(Equal("iframe"))
		Expect(w.Body.String()).To(ContainSubstring("window.__chiutilActionPostURL || '\\/submit'"))
		Expect(w.Body.String()).To(ContainSubstring("fetch(postURL"))
		Expect(w.Body.String()).NotTo(ContainSubstring("window.location.href"))
	})
})
