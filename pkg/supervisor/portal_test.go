package supervisor

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
)

var _ = Describe("portal", func() {
	// serve builds a supervisor HTTP server exposing one component, "hello",
	// optionally with a README file, and returns a non-redirecting client.
	serve := func(readme string) (*httptest.Server, *http.Client) {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		cfgs := []ComponentConfig{{Name: "hello", Description: "A simple HTTP server", Port: 18090, Readme: readme}}
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello", port: 18090}, nil, NewPaths(dir), bundle, cfgs, nil, BuildInfo{}, log.New("[t]", false))
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
		Expect(body).To(ContainSubstring("—"))            // last upgrade (em dash)
	})

	It("shows a compact stat line on each home card", func() {
		srv, client := serve("")
		defer srv.Close()

		code, body := get(client, srv.URL+"/")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("runs"))
		Expect(body).To(ContainSubstring("upgraded"))
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
})

// timeRFC returns an RFC3339 timestamp secondsAgo in the past.
func timeRFC(secondsAgo int) string {
	return time.Now().Add(-time.Duration(secondsAgo) * time.Second).UTC().Format(time.RFC3339)
}
