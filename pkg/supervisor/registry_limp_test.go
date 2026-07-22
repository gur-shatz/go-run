package supervisor

import (
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
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

	It("drops the memory persister in limp mode so samples cannot spam warnings", func() {
		cfg := Config{
			Memory:     &MemoryConfig{LimitBytes: 512 << 20},
			Components: []ComponentConfig{{Name: "api", Port: 8080, Command: "/bin/a"}},
		}
		cfg.ApplyDefaults()

		monitor := newMemoryMonitor(cfg, NewPaths(GinkgoT().TempDir()), false, newStatekitBundle(cfg), nil, log.New("[t]", false))
		if monitor == nil {
			Skip("memory subsystem unsupported on this platform")
		}
		Expect(monitor.persist).To(BeNil())

		writable := newMemoryMonitor(cfg, NewPaths(GinkgoT().TempDir()), true, newStatekitBundle(cfg), nil, log.New("[t]", false))
		Expect(writable.persist).NotTo(BeNil())
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
