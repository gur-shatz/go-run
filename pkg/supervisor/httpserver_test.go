package supervisor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
)

// stubStateProvider is a minimal stateProvider exposing a single component.
type stubStateProvider struct {
	name string
	port int
}

func (this stubStateProvider) Snapshot() SupervisorSnapshot {
	return SupervisorSnapshot{Components: []ComponentSnapshot{this.snap()}}
}

func (this stubStateProvider) ComponentSnapshot(name string) (ComponentSnapshot, bool) {
	if name == this.name {
		return this.snap(), true
	}
	return ComponentSnapshot{}, false
}

func (this stubStateProvider) snap() ComponentSnapshot {
	return ComponentSnapshot{Name: this.name, Status: "pass", GlobalState: "pass", UpdateStatus: "live", Port: this.port}
}

var _ = Describe("backoffice version", func() {
	It("serves build info at /backoffice/version and shows it on the index", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		build := BuildInfo{Version: "v1.2.3", Commit: "abc1234", Branch: "master", Date: "2026-06-02T09:00:00Z"}
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, NewPaths(dir), bundle, nil, nil, build, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		code, body := getBody(srv.Client(), srv.URL+"/backoffice/version")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("version: v1.2.3"))
		Expect(body).To(ContainSubstring("branch: master"))
		Expect(body).To(ContainSubstring("commit: abc1234"))

		// The backoffice index (JSON) carries the version in its description.
		_, idx := getBody(srv.Client(), srv.URL+"/backoffice/index.json")
		Expect(idx).To(ContainSubstring("Version v1.2.3 (branch master)"))
	})
})

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
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, paths, bundle, nil, nil, BuildInfo{}, log.New("[t]", false))
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
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, paths, bundle, nil, nil, BuildInfo{}, log.New("[t]", false))
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

var _ = Describe("component proxy", func() {
	// startServer wires a supervisor HTTP server whose only component, "hello",
	// points at the given port, and returns a client that does not follow redirects.
	startServer := func(componentPort int) (*httptest.Server, *http.Client) {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello", port: componentPort}, nil, NewPaths(dir), bundle, nil, nil, BuildInfo{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		client := srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return srv, client
	}

	It("forwards /proxy/<component>/* to the component port with the prefix stripped", func() {
		var gotPath, gotQuery, gotPrefix string
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotQuery = r.URL.RawQuery
			gotPrefix = r.Header.Get("X-Forwarded-Prefix")
			_, _ = w.Write([]byte("from-backend"))
		}))
		defer backend.Close()
		port, err := strconv.Atoi(must(url.Parse(backend.URL)).Port())
		Expect(err).NotTo(HaveOccurred())

		srv, client := startServer(port)
		defer srv.Close()

		// A deep path forwards verbatim regardless of segment depth: only the
		// /proxy/<component> mount prefix is removed.
		resp, err := client.Get(srv.URL + "/proxy/hello/api/v2/foo/bar/baz?x=1&y=2")
		Expect(err).NotTo(HaveOccurred())
		body := readBody(resp)

		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(body).To(Equal("from-backend"))
		Expect(gotPath).To(Equal("/api/v2/foo/bar/baz")) // /proxy/hello stripped, depth preserved
		Expect(gotQuery).To(Equal("x=1&y=2"))
		Expect(gotPrefix).To(Equal("/proxy/hello"))
	})

	It("redirects the bare /proxy/<component> to its trailing-slash form", func() {
		srv, client := startServer(1) // port unused: redirect happens before any dial
		defer srv.Close()

		resp, err := client.Get(srv.URL + "/proxy/hello")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusMovedPermanently))
		Expect(resp.Header.Get("Location")).To(Equal("hello/"))
	})

	It("404s an unknown component", func() {
		srv, client := startServer(1)
		defer srv.Close()

		resp, err := client.Get(srv.URL + "/proxy/nope/")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
	})

	It("502s when the component port is not listening", func() {
		// Pick a port nothing is on by opening then closing a listener.
		srv, client := startServer(1) // 127.0.0.1:1 — privileged, nothing listening
		defer srv.Close()

		resp, err := client.Get(srv.URL + "/proxy/hello/")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusBadGateway))
	})
})

func must(u *url.URL, err error) *url.URL {
	Expect(err).NotTo(HaveOccurred())
	return u
}

func readBody(resp *http.Response) string {
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}
