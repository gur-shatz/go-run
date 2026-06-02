package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
)

var _ = Describe("observer / health console", func() {
	newEnabled := func() Config {
		cfg := Config{StateDir: GinkgoT().TempDir()}
		cfg.Components = []ComponentConfig{{Name: "hello", Port: 18090, Command: "x", Remote: RemoteConfig{BaseURL: "file://x"}}}
		cfg.StateMonitor.Observe.Enabled = true
		cfg.ApplyDefaults()
		return cfg
	}

	It("mounts the console and API at /health and reflects ingested registry state", func() {
		cfg := newEnabled()
		bundle := newStatekitBundle(cfg)
		obs := newObserver(cfg.StateMonitor.Observe, bundle.registry, log.New("[t]", false))

		// One ingest pass: the same thing observer.Run does each tick.
		Expect(obs.store.IngestDocument(context.Background(), bundle.registry.StateDisplay(), time.Now())).To(Succeed())

		router := chi.NewRouter()
		obs.mount(router)
		srv := httptest.NewServer(router)
		defer srv.Close()
		client := srv.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

		// Console page.
		resp, err := client.Get(srv.URL + "/health/")
		Expect(err).NotTo(HaveOccurred())
		body := readBody(resp)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("STATEKIT_API_BASE"))
		Expect(body).To(ContainSubstring(`"api"`)) // relative API base (proxy-safe)

		// Bare /health -> /health/ (relative).
		r2, err := client.Get(srv.URL + "/health")
		Expect(err).NotTo(HaveOccurred())
		r2.Body.Close()
		Expect(r2.StatusCode).To(Equal(http.StatusMovedPermanently))
		Expect(r2.Header.Get("Location")).To(Equal("health/"))

		// API returns the ingested component state.
		code, apiBody := getBody(client, srv.URL+"/health/api/state/current")
		Expect(code).To(Equal(http.StatusOK))
		Expect(apiBody).To(ContainSubstring("hello"))
	})

	It("links the portal to the health console when the observer is enabled", func() {
		cfg := newEnabled()
		bundle := newStatekitBundle(cfg)
		obs := newObserver(cfg.StateMonitor.Observe, bundle.registry, log.New("[t]", false))
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello", port: 18090}, nil,
			NewPaths(cfg.StateDir), bundle, []ComponentConfig{{Name: "hello"}}, obs, BuildInfo{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		_, home := getBody(srv.Client(), srv.URL+"/")
		Expect(home).To(ContainSubstring(`href="health/"`))

		_, page := getBody(srv.Client(), srv.URL+"/components/hello/")
		Expect(page).To(ContainSubstring(`href="../../health/"`))
	})

	It("omits the health links when the observer is disabled", func() {
		dir := GinkgoT().TempDir()
		cfg := Config{StateDir: dir}
		cfg.ApplyDefaults()
		bundle := newStatekitBundle(cfg)
		hs := newHTTPServer("127.0.0.1:0", stubStateProvider{name: "hello", port: 18090}, nil,
			NewPaths(dir), bundle, []ComponentConfig{{Name: "hello"}}, nil, BuildInfo{}, log.New("[t]", false))
		srv := httptest.NewServer(hs.server.Handler)
		defer srv.Close()

		_, home := getBody(srv.Client(), srv.URL+"/")
		Expect(home).NotTo(ContainSubstring(`href="health/"`))
	})
})

func getBody(client *http.Client, url string) (int, string) {
	resp, err := client.Get(url)
	Expect(err).NotTo(HaveOccurred())
	return resp.StatusCode, readBody(resp)
}
