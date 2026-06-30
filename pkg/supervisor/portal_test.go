package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
)

type recordingControls struct {
	actions []string
}

func (this *recordingControls) StartComponent(_ context.Context, component string) error {
	this.actions = append(this.actions, "start:"+component)
	return nil
}

func (this *recordingControls) StopComponent(_ context.Context, component string) error {
	this.actions = append(this.actions, "stop:"+component)
	return nil
}

func (this *recordingControls) RestartComponent(_ context.Context, component string) error {
	this.actions = append(this.actions, "restart:"+component)
	return nil
}

var _ = Describe("portal", func() {
	// serve builds a supervisor HTTP server exposing one component, "hello",
	// optionally with a README file, and returns a non-redirecting client.
	serve := func(readme string) (*httptest.Server, *http.Client) {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		cfgs := []ComponentConfig{{Name: "hello", Description: "A simple HTTP server", Port: 18090, Readme: readme}}
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello", port: 18090}, nil, &recordingControls{}, NewPaths(dir), bundle, cfgs, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		client := srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return srv, client
	}

	get := func(client *http.Client, url string) (int, string) {
		resp, err := client.Get(url)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, readBody(resp)
	}

	It("renders a card per component on the home page with portal links", func() {
		srv, client := serve("")
		defer srv.Close()

		code, body := get(client, srv.URL+"/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("A simple HTTP server"))
		Expect(body).To(ContainSubstring(`href="components/hello/"`)) // drill-down (relative)
		Expect(body).To(ContainSubstring(`href="proxy/hello/"`))      // open app
		Expect(body).To(ContainSubstring(`href="backoffice/"`))       // backoffice
		Expect(body).To(ContainSubstring(`badge pass`))               // status badge
		Expect(body).To(ContainSubstring(`<meta http-equiv="refresh" content="20">`))
	})

	It("renders proxy_url links on cards and component pages", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		sp := stubStateProvider{name: "hello", port: 18090, proxyURLs: map[string]string{"admin": ":18091/admin"}}
		hs := newHTTPServer("127.0.0.1:0", sp, nil, &recordingControls{}, NewPaths(dir), bundle, []ComponentConfig{{Name: "hello"}}, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		code, body := get(srv.Client(), srv.URL+"/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring(`href="proxyurls/hello/admin/"`))

		code, body = get(srv.Client(), srv.URL+"/components/hello/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring(`href="../../proxyurls/hello/admin/"`))
	})

	It("renders component lifecycle controls and posts them through the portal", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		controls := &recordingControls{}
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello", port: 18090}, nil, controls, NewPaths(dir), bundle, []ComponentConfig{{Name: "hello"}}, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()
		client := srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

		code, body := get(client, srv.URL+"/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring(`action="components/hello/start"`))
		Expect(body).To(ContainSubstring(`action="components/hello/stop"`))
		Expect(body).To(ContainSubstring(`action="components/hello/restart"`))

		resp, err := client.PostForm(srv.URL+"/components/hello/stop", nil)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusSeeOther))
		Expect(resp.Header.Get("Location")).To(Equal("/"))
		Expect(controls.actions).To(ContainElement("stop:hello"))
	})

	It("renders external components without lifecycle controls", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		hs := newHTTPServer("127.0.0.1:0",
			stubStateProvider{name: "docs", external: true, url: "http://docs.internal:8080", globalState: "pass", status: "pass", statusReason: "ok"},
			nil, &recordingControls{}, NewPaths(dir), bundle, nil,
			[]ExternalComponentConfig{{Name: "docs", Description: "Docs service", URL: "http://docs.internal:8080"}},
			nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		code, body := get(srv.Client(), srv.URL+"/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("Docs service"))
		Expect(body).To(ContainSubstring("external"))
		Expect(body).To(ContainSubstring(`href="proxy/docs/"`))
		Expect(body).NotTo(ContainSubstring(`action="components/docs/start"`))
		Expect(body).NotTo(ContainSubstring(`action="components/docs/stop"`))
		Expect(body).NotTo(ContainSubstring(`action="components/docs/restart"`))
	})

	It("renders the README as HTML on the component page", func() {
		dir := GinkgoT().TempDir()
		readme := filepath.Join(dir, "hello.md")
		md := "# Hello\n\nUse the **greet** endpoint.\n\n" +
			"| Path | Description |\n| ---- | ----------- |\n| `/greet` | greeting |\n"
		Expect(os.WriteFile(readme, []byte(md), 0o644)).To(Succeed())

		srv, client := serve(readme)
		defer srv.Close()

		code, body := get(client, srv.URL+"/components/hello/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("<h1>Hello</h1>"))
		Expect(body).To(ContainSubstring("<strong>greet</strong>"))
		// GFM pipe table renders to a real <table>, not plain text.
		Expect(body).To(ContainSubstring("<table>"))
		Expect(body).To(ContainSubstring("<th>Path</th>"))
		Expect(body).To(ContainSubstring("<td>greeting</td>"))
		// Links from the component page climb back to the root (../../).
		Expect(body).To(ContainSubstring(`href="../../proxy/hello/"`))
	})

	It("shows health detail (uptime, runs, last upgrade) on the component page", func() {
		srv, client := serve("")
		defer srv.Close()

		code, body := get(client, srv.URL+"/components/hello/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("Uptime"))
		Expect(body).To(ContainSubstring("Runs"))
		Expect(body).To(ContainSubstring("Last upgrade"))
		Expect(body).To(ContainSubstring("Fast crashes"))
		Expect(body).To(ContainSubstring("Exec failures"))
		// With no running child the stub reports zeroes: humanized defaults.
		Expect(body).To(ContainSubstring("not running")) // uptime
		Expect(body).To(ContainSubstring("—"))           // last upgrade (em dash)
	})

	It("shows a compact stat line on each home card", func() {
		srv, client := serve("")
		defer srv.Close()

		code, body := get(client, srv.URL+"/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("runs"))
		Expect(body).To(ContainSubstring("upgraded"))
	})

	It("shows run and update states independently on the home card", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		sp := stubStateProvider{
			name:         "hello",
			port:         18090,
			globalState:  "pass",
			status:       "fail",
			statusReason: "exec failed: exit status 1",
			updateState:  "warn",
			updateReason: "target v3 is rejected; holding current",
		}
		hs := newHTTPServer("127.0.0.1:0", sp, nil, &recordingControls{}, NewPaths(dir), bundle, []ComponentConfig{{Name: "hello"}}, nil, nil, nil, BuildInfo{}, BasicAuthConfig{}, FaviconConfig{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		code, body := get(srv.Client(), srv.URL+"/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring(`badge fail`))
		Expect(body).To(ContainSubstring(`Run</span>`))
		Expect(body).To(ContainSubstring(`mini-badge fail`))
		Expect(body).To(ContainSubstring("exec failed: exit status 1"))
		Expect(body).To(ContainSubstring(`Update</span>`))
		Expect(body).To(ContainSubstring(`mini-badge warn`))
		Expect(body).To(ContainSubstring("target v3 is rejected; holding current"))
		Expect(sp.snap().GlobalState).To(Equal("pass"))
	})

	It("notes when no README is configured", func() {
		srv, client := serve("")
		defer srv.Close()

		code, body := get(client, srv.URL+"/components/hello/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("No README configured"))
	})

	It("redirects the bare /components/<name> to its trailing-slash form", func() {
		srv, client := serve("")
		defer srv.Close()

		resp, err := client.Get(srv.URL + "/components/hello")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusMovedPermanently))
		Expect(resp.Header.Get("Location")).To(Equal("hello/"))
	})

	It("404s an unknown component", func() {
		srv, client := serve("")
		defer srv.Close()

		code, _ := get(client, srv.URL+"/components/nope/")
		Expect(code).To(Equal(http.StatusNotFound))
	})

	DescribeTable("fmtUptime humanizes seconds",
		func(secs int64, want string) { Expect(fmtUptime(secs)).To(Equal(want)) },
		Entry("zero", int64(0), "not running"),
		Entry("seconds", int64(42), "42s"),
		Entry("minutes", int64(125), "2m"),
		Entry("hours", int64(7325), "2h 2m"),
		Entry("days", int64(90000), "1d 1h"),
	)

	It("fmtAgo renders relative times and absent timestamps", func() {
		Expect(fmtAgo("")).To(Equal("—"))
		Expect(fmtAgo("not-a-time")).To(Equal("not-a-time"))
		Expect(fmtAgo(timeRFC(30))).To(Equal("just now")) // under a minute
		Expect(fmtAgo(timeRFC(5 * 60))).To(Equal("5m ago"))
	})

	DescribeTable("portalDisplayState downgrades only",
		func(global, run, update, want string) {
			Expect(portalDisplayState(global, run, update, "")).To(Equal(want))
		},
		Entry("healthy app, run, and updater", "pass", "pass", "pass", "pass"),
		Entry("healthy app and rejected target", "pass", "pass", "warn", "warn"),
		Entry("failed run downgrades healthy app", "pass", "fail", "pass", "fail"),
		Entry("warn app stays warn", "warn", "pass", "warn", "warn"),
		Entry("fail app is not softened", "fail", "pass", "warn", "fail"),
		Entry("down app is not softened", "down", "pass", "warn", "down"),
		Entry("update failure is capped at warn", "pass", "pass", "fail", "warn"),
	)

	DescribeTable("portalDisplayState escalates on memory pressure",
		func(memory, want string) {
			Expect(portalDisplayState("pass", "pass", "pass", memory)).To(Equal(want))
		},
		Entry("ok does not escalate", "ok", "pass"),
		Entry("tracking-only does not escalate", "", "pass"),
		Entry("soft escalates to warn", "soft", "warn"),
		Entry("hard escalates to fail", "hard", "fail"),
	)
})

// timeRFC returns an RFC3339 timestamp secondsAgo in the past.
func timeRFC(secondsAgo int) string {
	return time.Now().Add(-time.Duration(secondsAgo) * time.Second).UTC().Format(time.RFC3339)
}
