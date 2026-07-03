package supervisor

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gur-shatz/statekit"
	"github.com/gur-shatz/statekit/console"
	"github.com/gur-shatz/statekit/storage"

	"github.com/gur-shatz/go-run/internal/log"
)

// observer is the supervisor's optional health-aggregation role. It owns a
// statekit storage fed from the supervisor's own registry on a ticker, and
// serves the fleet state console + API at /health.
//
// This deliberately reuses the existing scraper path: the scraper already
// mirrors every component's /state into the registry, so the observer just
// snapshots registry.StateDisplay() into the store. The store decomposes each
// snapshot into current state, per-target rollups, and bounded history.
// Current state is in-memory and rebuilds from the first scrape after a
// restart; with history_dir set, the timeline chart and the
// transition/incident history persist under it and survive restarts.
type observer struct {
	store          *storage.MemoryStore
	registry       *statekit.Registry
	ingestInterval time.Duration
	logger         *log.Logger
}

// newObserver builds the store. Run must be called to start ingesting, and
// mount to expose the console. A history_dir that cannot be opened degrades
// that piece to in-memory with a warning: the health console must never
// block supervision.
func newObserver(cfg ObserveConfig, registry *statekit.Registry, logger *log.Logger) *observer {
	opts := []storage.MemoryStoreOption{storage.WithDocumentCache(
		storage.NewFreecacheDocumentCache[statekit.StateDisplayDocument](cfg.CacheMB<<20),
		5*time.Minute,
	)}
	if cfg.HistoryDir != "" {
		chart, err := storage.NewFileChartStore(filepath.Join(cfg.HistoryDir, "chart"), time.Minute, 24*60)
		if err != nil {
			logger.Warn("observer: chart history disabled, falling back to memory: %v", err)
		} else {
			opts = append(opts, storage.WithChartStore(chart))
		}
		journal, err := storage.OpenJournal(filepath.Join(cfg.HistoryDir, "journal.ndjson"))
		if err != nil {
			logger.Warn("observer: transition/incident history disabled, falling back to memory: %v", err)
		} else {
			opts = append(opts, storage.WithJournal(journal))
		}
	}
	return &observer{
		store:          storage.NewMemoryStore(opts...),
		registry:       registry,
		ingestInterval: cfg.IngestInterval,
		logger:         logger,
	}
}

// mount wires the dedicated health console at /health/ and the storage API at
// /health/api/ on the root router. The console is a self-contained app (its
// own HTML/CSS/JS), so it gets its own top-level mount rather than living
// inside the backoffice index. The API base is relative ("api"), so the
// console works behind a path-stripping reverse proxy.
func (this *observer) mount(router chi.Router) {
	api := storage.NewAPI(this.store)
	ui := console.Handler(console.Options{Title: "supervisor health", APIBase: "api"})

	// One inner mux dispatches /api/* to the storage API and everything else
	// (/, /app.css, /app.js) to the console, so the chi router needs only a
	// single /health/* catch-all (no overlapping wildcards).
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
}

// Run snapshots the registry into the store every ingest interval until ctx is
// cancelled. The first ingest happens immediately so the console isn't empty.
func (this *observer) Run(ctx context.Context) {
	t := time.NewTicker(this.ingestInterval)
	defer t.Stop()
	for {
		if err := this.store.IngestDocument(ctx, this.registry.StateDisplay(), time.Now()); err != nil {
			this.logger.Warn("observer ingest: %v", err)
		}
		this.ingestEscalations(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ingestEscalations mirrors the registry's incidents (deploy, rollback,
// crash episodes) into the store so they appear on the /health console as
// incident rows and timeline markers. The full document is read without an
// after/ack cursor: the store dedupes events, and the cursor stays untouched
// for an upstream fleet scraper consuming /backoffice/escalations. Each
// incident is attributed to its component (from the "component" topic) so it
// lines up with the console's per-target rows.
func (this *observer) ingestEscalations(ctx context.Context) {
	doc := this.registry.EscalationDisplay("", "")
	if len(doc.Incidents) == 0 {
		return
	}
	for i := range doc.Incidents {
		if doc.Incidents[i].ScrapedFrom != "" {
			continue
		}
		if component, ok := doc.Incidents[i].Topics["component"].(string); ok {
			doc.Incidents[i].ScrapedFrom = component
		}
	}
	if err := this.store.IngestEscalations(ctx, "supervisor", doc, time.Now()); err != nil {
		this.logger.Warn("observer escalations ingest: %v", err)
	}
}
