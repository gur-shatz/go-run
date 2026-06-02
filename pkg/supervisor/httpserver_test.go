package supervisor

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
)

// stubStateProvider is a minimal stateProvider exposing a single component.
type stubStateProvider struct{ name string }

func (this stubStateProvider) Snapshot() SupervisorSnapshot {
	return SupervisorSnapshot{Components: []ComponentSnapshot{{Name: this.name, Status: "pass"}}}
}

func (this stubStateProvider) ComponentSnapshot(name string) (ComponentSnapshot, bool) {
	if name == this.name {
		return ComponentSnapshot{Name: this.name}, true
	}
	return ComponentSnapshot{}, false
}

var _ = Describe("current_version_logs", func() {
	It("redirects to the live version's log dir with a proxy-safe relative Location", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		paths := NewPaths(dir)

		Expect(paths.Component("hello").EnsureDirs()).To(Succeed())
		Expect(paths.Component("hello").WriteCurrent("v123")).To(Succeed())
		verLogs := paths.LogsForVersion("hello", "v123")
		Expect(os.MkdirAll(verLogs, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(verLogs, "stdout.log"), []byte("hi\n"), 0o644)).To(Succeed())

		bundle := newStatekitBundle(cfg)
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, paths, bundle, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()
		client := srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

		resp, err := client.Get(srv.URL + "/backoffice/components/hello/current_version_logs")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()

		// Relative (not absolute /backoffice/...) so it survives a path-stripping
		// reverse proxy: "../../" climbs from components/hello/ to the backoffice root.
		Expect(resp.StatusCode).To(Equal(http.StatusFound))
		Expect(resp.Header.Get("Location")).To(Equal("../../logs/hello/v123/"))

		// The browser-resolved target serves the version log dir.
		target, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
		Expect(err).NotTo(HaveOccurred())
		Expect(target.Path).To(Equal("/backoffice/logs/hello/v123/"))

		landed, err := client.Get(target.String())
		Expect(err).NotTo(HaveOccurred())
		defer landed.Body.Close()
		Expect(landed.StatusCode).To(Equal(http.StatusOK))
	})

	It("404s when no current version is on disk", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		paths := NewPaths(dir)
		Expect(paths.Component("hello").EnsureDirs()).To(Succeed())

		bundle := newStatekitBundle(cfg)
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, paths, bundle, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()
		client := srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

		resp, err := client.Get(srv.URL + "/backoffice/components/hello/current_version_logs")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
	})
})
