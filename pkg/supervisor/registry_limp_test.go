package supervisor

import (
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("statekit bundle limp-mode surface", func() {
	It("exposes supervisor_state_writable 0 and a warn leaf once degradation is observed", func() {
		bundle := newStatekitBundle(Config{})
		bundle.observeStateUnwritable("state dir not writable: probe failed")

		rec := httptest.NewRecorder()
		bundle.registry.PrometheusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		Expect(rec.Body.String()).To(ContainSubstring("supervisor_state_writable 0"))

		rec = httptest.NewRecorder()
		bundle.registry.StateDisplayYAMLHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/state", nil))
		Expect(rec.Body.String()).To(SatisfyAll(
			ContainSubstring("state.writable"),
			ContainSubstring("state dir not writable"),
		))
	})

	It("keeps the healthy surface unchanged (no state.writable leaf, no gauge)", func() {
		bundle := newStatekitBundle(Config{})

		rec := httptest.NewRecorder()
		bundle.registry.PrometheusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		Expect(rec.Body.String()).NotTo(ContainSubstring("supervisor_state_writable"))

		rec = httptest.NewRecorder()
		bundle.registry.StateDisplayYAMLHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/state", nil))
		Expect(rec.Body.String()).NotTo(ContainSubstring("state.writable"))
	})
})
