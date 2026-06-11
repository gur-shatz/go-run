package supervisor

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
)

// stubStateProvider is a minimal stateProvider exposing a single component.
type stubStateProvider struct {
	name         string
	port         int
	external     bool
	url          string
	globalState  string
	status       string
	statusReason string
	updateState  string
	updateReason string
	proxyURLs    map[string]string
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
	globalState := this.globalState
	if globalState == "" {
		globalState = "pass"
	}
	updateState := this.updateState
	if updateState == "" {
		updateState = "pass"
	}
	status := this.status
	if status == "" {
		status = "pass"
	}
	updateStatus := "live"
	if this.external {
		updateStatus = ""
	}
	return ComponentSnapshot{Name: this.name, External: this.external, URL: this.url, Status: status, StatusReason: this.statusReason, GlobalState: globalState, UpdateStatus: updateStatus, UpdateState: updateState, UpdateReason: this.updateReason, Port: this.port, ProxyURLs: this.proxyURLs}
}

var _ = Describe("backoffice version", func() {
	It("serves build info at /backoffice/version and shows it on the index", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		build := BuildInfo{Version: "v1.2.3", Commit: "abc1234", Branch: "master", Date: "2026-06-02T09:00:00Z"}
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, nil, NewPaths(dir), bundle, nil, nil, nil, build, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
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

var _ = Describe("backoffice env", func() {
	It("serves sorted YAML and redacts sensitive variable names", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)

		oldEnv := os.Environ()
		os.Clearenv()
		defer func() {
			os.Clearenv()
			for _, item := range oldEnv {
				name, value, _ := strings.Cut(item, "=")
				Expect(os.Setenv(name, value)).To(Succeed())
			}
		}()
		Expect(os.Setenv("Z_VISIBLE", "shown")).To(Succeed())
		Expect(os.Setenv("API_KEY", "abc123")).To(Succeed())
		Expect(os.Setenv("db_password", "pw123")).To(Succeed())
		Expect(os.Setenv("CLIENT_SECRET_VALUE", "secret123")).To(Succeed())

		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, nil, NewPaths(dir), bundle, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		code, body := getBody(srv.Client(), srv.URL+"/backoffice/env")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("env:"))
		Expect(body).To(ContainSubstring("name: API_KEY"))
		Expect(body).To(ContainSubstring("name: CLIENT_SECRET_VALUE"))
		Expect(body).To(ContainSubstring("name: db_password"))
		Expect(body).To(ContainSubstring("name: Z_VISIBLE"))
		Expect(body).To(ContainSubstring("value: '[REDACTED]'"))
		Expect(body).To(ContainSubstring("value: shown"))
		Expect(body).NotTo(ContainSubstring("abc123"))
		Expect(body).NotTo(ContainSubstring("pw123"))
		Expect(body).NotTo(ContainSubstring("secret123"))
		Expect(body).To(MatchRegexp(`(?s)name: API_KEY.*name: CLIENT_SECRET_VALUE.*name: Z_VISIBLE.*name: db_password`))
	})

	It("redacts names containing SECRET, PASSWORD, or KEY case-insensitively", func() {
		env := buildEnv([]string{
			"plain=value",
			"HAS_SECRET=value",
			"has_password=value",
			"ssh_key=value",
		})

		values := map[string]string{}
		for _, entry := range env.Env {
			values[entry.Name] = entry.Value
		}
		Expect(values["plain"]).To(Equal("value"))
		Expect(values["HAS_SECRET"]).To(Equal("[REDACTED]"))
		Expect(values["has_password"]).To(Equal("[REDACTED]"))
		Expect(values["ssh_key"]).To(Equal("[REDACTED]"))
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
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, nil, paths, bundle, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
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
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, nil, paths, bundle, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
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

var _ = Describe("component controls", func() {
	It("exposes start, stop, and restart POST endpoints under backoffice", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		controls := &recordingControls{}
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, controls, NewPaths(dir), bundle, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		for _, action := range []string{"start", "stop", "restart"} {
			resp, err := srv.Client().Post(srv.URL+"/backoffice/components/hello/"+action, "application/x-www-form-urlencoded", nil)
			Expect(err).NotTo(HaveOccurred())
			_ = resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusNoContent))
		}

		Expect(controls.actions).To(Equal([]string{"start:hello", "stop:hello", "restart:hello"}))
	})
})

var _ = Describe("favicon", func() {
	newFaviconServer := func(status string, auth BasicAuthConfig) (*httptest.Server, *http.Client) {
		dir := GinkgoT().TempDir()
		cfg := Config{
			StateDir: dir,
			Components: []ComponentConfig{
				{Name: "hello", Port: 18090, Command: "/bin/hello"},
			},
		}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		switch status {
		case "pass":
			bundle.markPass("hello", "ok")
		case "warn":
			bundle.markWarn("hello", "degraded")
		case "fail":
			bundle.markFail("hello", "failed")
		case "down":
			bundle.markDown("hello", "down")
		}
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, nil, NewPaths(dir), bundle, nil, nil, nil, BuildInfo{}, auth, FaviconConfig{Name: "sv"}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		client := srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return srv, client
	}

	It("serves the configured two-letter SVG favicon with no-cache headers", func() {
		srv, client := newFaviconServer("pass", BasicAuthConfig{})
		defer srv.Close()

		resp, err := client.Get(srv.URL + "/favicon.ico")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()

		body := readBody(resp)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Header.Get("Content-Type")).To(HavePrefix("image/svg+xml"))
		Expect(resp.Header.Get("Cache-Control")).To(ContainSubstring("no-cache"))
		Expect(body).To(ContainSubstring(">SV</text>"))
		Expect(body).To(ContainSubstring(`fill="#16a34a"`))
	})

	It("switches the background color from green to yellow to red by state severity", func() {
		cases := []struct {
			status string
			color  string
		}{
			{status: "pass", color: "#16a34a"},
			{status: "warn", color: "#facc15"},
			{status: "fail", color: "#dc2626"},
			{status: "down", color: "#dc2626"},
		}
		for _, tc := range cases {
			srv, client := newFaviconServer(tc.status, BasicAuthConfig{})
			resp, err := client.Get(srv.URL + "/favicon.ico")
			Expect(err).NotTo(HaveOccurred())
			body := readBody(resp)
			srv.Close()

			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(body).To(ContainSubstring(`fill="` + tc.color + `"`))
		}
	})

	It("is available before login when basic auth is enabled", func() {
		auth := BasicAuthConfig{Enabled: true, Username: "op", Password: "s3cret"}
		srv, client := newFaviconServer("pass", auth)
		defer srv.Close()

		resp, err := client.Get(srv.URL + "/favicon.ico")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	// Health probes must stay reachable with the auth gate on: a kubelet
	// liveness/readiness probe carries no session cookie or credentials, so a
	// gated healthz returns 401/303 and the orchestrator crash-loops the pod.
	It("serves /backoffice/healthz without auth when basic auth is enabled", func() {
		auth := BasicAuthConfig{Enabled: true, Username: "op", Password: "s3cret"}
		srv, client := newFaviconServer("pass", auth)
		defer srv.Close()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

		resp, err := client.Get(srv.URL + "/backoffice/healthz")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
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
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello", port: componentPort}, nil, nil, NewPaths(dir), bundle, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
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

	It("forwards external components to their configured URL", func() {
		var gotPath, gotPrefix string
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotPrefix = r.Header.Get("X-Forwarded-Prefix")
			_, _ = w.Write([]byte("from-external"))
		}))
		defer backend.Close()

		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "docs", external: true, url: backend.URL}, nil, nil, NewPaths(dir), bundle, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		resp, err := srv.Client().Get(srv.URL + "/proxy/docs/api")
		Expect(err).NotTo(HaveOccurred())
		body := readBody(resp)

		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(body).To(Equal("from-external"))
		Expect(gotPath).To(Equal("/api"))
		Expect(gotPrefix).To(Equal("/proxy/docs"))
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

var _ = Describe("proxy urls", func() {
	It("forwards a named :port/path target with the configured path as a prefix", func() {
		var gotPath, gotQuery, gotPrefix string
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotQuery = r.URL.RawQuery
			gotPrefix = r.Header.Get("X-Forwarded-Prefix")
			_, _ = w.Write([]byte("from-proxy-url"))
		}))
		defer backend.Close()
		port, err := strconv.Atoi(must(url.Parse(backend.URL)).Port())
		Expect(err).NotTo(HaveOccurred())

		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{
			name:      "hello",
			port:      18090,
			proxyURLs: map[string]string{"admin": fmt.Sprintf(":%d/base/path?fixed=1", port)},
		}, nil, nil, NewPaths(dir), bundle, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		resp, err := srv.Client().Get(srv.URL + "/proxyurls/hello/admin/deep/item?x=2")
		Expect(err).NotTo(HaveOccurred())
		body := readBody(resp)

		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(body).To(Equal("from-proxy-url"))
		Expect(gotPath).To(Equal("/base/path/deep/item"))
		Expect(gotQuery).To(Equal("fixed=1&x=2"))
		Expect(gotPrefix).To(Equal("/proxyurls/hello/admin"))
	})

	It("forwards absolute targets", func() {
		var gotPath string
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_, _ = w.Write([]byte("absolute"))
		}))
		defer backend.Close()

		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{
			name:      "hello",
			port:      18090,
			proxyURLs: map[string]string{"app": backend.URL + "/admin"},
		}, nil, nil, NewPaths(dir), bundle, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		resp, err := srv.Client().Get(srv.URL + "/proxyurls/hello/app/settings")
		Expect(err).NotTo(HaveOccurred())
		body := readBody(resp)

		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(body).To(Equal("absolute"))
		Expect(gotPath).To(Equal("/admin/settings"))
	})

	It("404s unknown named proxy urls", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello", port: 18090}, nil, nil, NewPaths(dir), bundle, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		resp, err := srv.Client().Get(srv.URL + "/proxyurls/hello/admin/")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
	})
})

var _ = Describe("login gate", func() {
	// newAuthedServer wires a supervisor HTTP server with the login gate
	// enabled for user "op" / pass "s3cret".
	newAuthedServer := func() *httptest.Server {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		auth := BasicAuthConfig{Enabled: true, Username: "op", Password: "s3cret", Hint: "demo login: op / s3cret"}
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello"}, nil, nil, NewPaths(dir), bundle, nil, nil, nil, BuildInfo{}, auth, FaviconConfig{}, log.New("[t]", false))
		return httptest.NewServer(hs.server.Handler)
	}

	// noRedirect returns a client that surfaces redirects instead of following.
	noRedirect := func(srv *httptest.Server) *http.Client {
		client := srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return client
	}

	It("redirects an unauthenticated request to the login form", func() {
		srv := newAuthedServer()
		defer srv.Close()

		resp, err := noRedirect(srv).Get(srv.URL + "/backoffice/version")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusSeeOther))
		Expect(resp.Header.Get("Location")).To(HavePrefix("/login"))
		Expect(resp.Header.Get("Location")).To(ContainSubstring("next=%2Fbackoffice%2Fversion"))
	})

	It("challenges unauthenticated git-style requests with HTTP Basic", func() {
		srv := newAuthedServer()
		defer srv.Close()

		req, err := http.NewRequest(http.MethodGet, srv.URL+"/backoffice/version", nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("User-Agent", "git/2.45.0")
		req.Header.Set("Accept", "*/*")
		resp, err := noRedirect(srv).Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		Expect(resp.Header.Get("WWW-Authenticate")).To(Equal(`Basic realm="supervisor"`))
	})

	It("accepts HTTP Basic authentication with the configured credentials", func() {
		srv := newAuthedServer()
		defer srv.Close()

		req, err := http.NewRequest(http.MethodGet, srv.URL+"/backoffice/version", nil)
		Expect(err).NotTo(HaveOccurred())
		req.SetBasicAuth("op", "s3cret")
		resp, err := srv.Client().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("rejects wrong HTTP Basic credentials with a Basic challenge", func() {
		srv := newAuthedServer()
		defer srv.Close()

		req, err := http.NewRequest(http.MethodGet, srv.URL+"/backoffice/version", nil)
		Expect(err).NotTo(HaveOccurred())
		req.SetBasicAuth("op", "wrong")
		resp, err := noRedirect(srv).Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		Expect(resp.Header.Get("WWW-Authenticate")).To(Equal(`Basic realm="supervisor"`))
	})

	It("serves a login form that shows the hint", func() {
		srv := newAuthedServer()
		defer srv.Close()

		code, body := getBody(srv.Client(), srv.URL+"/login")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("<form"))
		Expect(body).To(ContainSubstring("demo login: op / s3cret"))
	})

	It("re-renders the form with an error on wrong credentials", func() {
		srv := newAuthedServer()
		defer srv.Close()

		resp, err := noRedirect(srv).PostForm(srv.URL+"/login",
			url.Values{"username": {"op"}, "password": {"nope"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		Expect(readBody(resp)).To(ContainSubstring("Incorrect username or password"))
	})

	It("mints a session cookie on success and grants access", func() {
		srv := newAuthedServer()
		defer srv.Close()
		jar, err := cookiejar.New(nil)
		Expect(err).NotTo(HaveOccurred())
		client := &http.Client{Jar: jar}

		// Posting good credentials redirects to ?next and the follow lands 200.
		resp, err := client.PostForm(srv.URL+"/login",
			url.Values{"username": {"op"}, "password": {"s3cret"}, "next": {"/backoffice/version"}})
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Request.URL.Path).To(Equal("/backoffice/version"))

		// The cookie now carries a fresh request through on its own.
		code, _ := getBody(client, srv.URL+"/backoffice/version")
		Expect(code).To(Equal(http.StatusOK))
	})

	It("exempts the healthz probe from the gate", func() {
		// Health probes must stay open even with the gate on: a kubelet probe
		// carries no cookie/credentials, so gating healthz would crash-loop the
		// pod. healthz exposes nothing sensitive, so it is safe to leave open.
		srv := newAuthedServer()
		defer srv.Close()

		resp, err := noRedirect(srv).Get(srv.URL + "/backoffice/healthz")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("rejects a cookie whose timestamp is too old", func() {
		srv := newAuthedServer()
		defer srv.Close()

		// A signature the server would accept, but minted 13h ago — past the
		// 12h max age. Same credentials => same deterministic signature.
		stale := newAuthGate(BasicAuthConfig{Username: "op", Password: "s3cret"}).
			mint(time.Now().Add(-13 * time.Hour))
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/backoffice/version", nil)
		req.AddCookie(&http.Cookie{Name: authCookieName, Value: stale})

		resp, err := noRedirect(srv).Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusSeeOther))
	})

	It("rejects a cookie with a tampered signature", func() {
		srv := newAuthedServer()
		defer srv.Close()

		forged := fmt.Sprintf("%d.deadbeef", time.Now().Unix())
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/backoffice/version", nil)
		req.AddCookie(&http.Cookie{Name: authCookieName, Value: forged})

		resp, err := noRedirect(srv).Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusSeeOther))
	})

	It("logs out by clearing the session cookie", func() {
		srv := newAuthedServer()
		defer srv.Close()

		resp, err := noRedirect(srv).Get(srv.URL + "/logout")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusSeeOther))
		Expect(resp.Header.Get("Set-Cookie")).To(ContainSubstring(authCookieName + "=;"))
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
