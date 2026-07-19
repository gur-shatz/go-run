package runctl

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gur-shatz/statekit"
	"github.com/gur-shatz/statekit/console"
	"github.com/gur-shatz/statekit/scraper"
	"github.com/gur-shatz/statekit/storage"
)

// StateMonitor is runctl's statekit-based health aggregation. It scrapes each
// target's backoffice through runctl's own reverse proxy
// (/api/targets/{name}/backoffice/...) for /state and /metrics, mirrors the
// results into a local statekit registry, and serves the health console at
// /health plus the aggregate /state (YAML) and /metrics (Prometheus).
//
// It is deliberately scrape-only: runctl's own target lifecycle tracking
// (TargetStatus, heartbeat, runui) is untouched. A target whose backoffice is
// down, or which exposes no statekit surfaces, contributes only its
// "responsive" liveness check; scraped state and metrics appear when the
// child serves them.
type StateMonitor struct {
	registry       *statekit.Registry
	store          *storage.MemoryStore
	sc             *scraper.Scraper
	title          string
	ingestInterval time.Duration
}

// NewStateMonitor builds the registry, scraper, and in-memory store from the
// runctl config. Run must be called to start scraping and ingesting, and
// Mount to expose the console and aggregate endpoints.
func NewStateMonitor(cfg Config) (*StateMonitor, error) {
	registry := statekit.NewRegistry(statekit.WithLabel("service", "runctl"))

	store := storage.NewMemoryStore(
		storage.WithDocumentCache(
			storage.NewFreecacheDocumentCache[statekit.StateDisplayDocument](32<<20),
			5*time.Minute,
		),
		storage.WithMetricsStore(storage.NewMemoryMetricsStore(0, 0)),
	)

	scfg := scraper.Config{
		Defaults: scraper.Defaults{
			Interval:   scraper.Duration(cfg.Monitor.Interval),
			Timeout:    scraper.Duration(cfg.Monitor.Timeout),
			Expiration: scraper.Duration(cfg.Monitor.Expiration),
		},
	}

	names := make([]string, 0, len(cfg.Targets))
	for name, tcfg := range cfg.Targets {
		if tcfg.IsMonitored() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		// Scrape through runctl's own backoffice proxy: the child's UDS
		// address is only discovered at runtime, but the proxy URL is static.
		// While the backoffice is unavailable the proxy answers 503, which
		// surfaces as a failed responsive check.
		base := fmt.Sprintf("http://127.0.0.1:%d/api/targets/%s/backoffice",
			cfg.API.Port, url.PathEscape(name))
		scfg.Targets = append(scfg.Targets, scraper.TargetConfig{
			Name:    name,
			BaseURL: base,
			Liveness: []scraper.LivenessTask{{
				ID:            "responsive",
				Path:          "/info",
				ExpectStatus:  []int{200},
				FailurePolicy: scraper.FailurePolicy{FailAfter: 1, RecoverAfter: 1},
			}},
			StateAggregation: &scraper.StateAggregationTask{Path: "/state"},
			Metrics:          &scraper.MetricsTask{Paths: []string{"/metrics"}},
		})
	}

	sc, err := scraper.New(scfg, scraper.WithMetricsIngestor(store.MetricsStore()))
	if err != nil {
		return nil, fmt.Errorf("build monitor scraper: %w", err)
	}
	for _, st := range sc.States() {
		if err := registry.Register(st); err != nil {
			return nil, fmt.Errorf("register scraped state %q: %w", st.Name(), err)
		}
	}
	if err := registry.RegisterCollectors(sc.MetricsCollector()); err != nil {
		return nil, fmt.Errorf("register scraped metrics: %w", err)
	}

	title := cfg.Title
	if title == "" {
		title = "runctl"
	}
	return &StateMonitor{
		registry:       registry,
		store:          store,
		sc:             sc,
		title:          title + " health",
		ingestInterval: time.Second,
	}, nil
}

// Mount wires the health console at /health/, its storage API at /health/api/,
// and the aggregate /state and /metrics endpoints on the given router.
func (this *StateMonitor) Mount(router chi.Router) {
	api := storage.NewAPI(this.store)
	ui := console.Handler(console.Options{Title: this.title, APIBase: "api"})

	// One inner mux dispatches /api/* to the storage API and everything else
	// (/, /app.css, /app.js) to the console, so the chi router needs only a
	// single /health/* catch-all.
	inner := http.NewServeMux()
	inner.Handle("/api/", http.StripPrefix("/api", api.Handler()))
	inner.Handle("/", ui)

	router.Handle("/health/*", http.StripPrefix("/health", inner))

	// Bare /health -> /health/ (relative, proxy-safe) so the console's
	// relative asset + API links resolve.
	router.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "health/")
		w.WriteHeader(http.StatusMovedPermanently)
	})

	router.Method(http.MethodGet, "/state", this.registry.StateDisplayYAMLHandler())
	router.Method(http.MethodGet, "/metrics", this.registry.PrometheusHandler())
}

// Run starts the scraper's task loops and the ingest ticker that snapshots
// the registry into the store for the console. Blocks until ctx is cancelled.
func (this *StateMonitor) Run(ctx context.Context) {
	go this.sc.Run(ctx)

	t := time.NewTicker(this.ingestInterval)
	defer t.Stop()
	for {
		if err := this.store.IngestDocument(ctx, this.registry.StateDisplay(), time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "[runctl] monitor ingest: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
