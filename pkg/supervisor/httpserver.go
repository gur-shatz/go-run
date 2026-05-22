package supervisor

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gur-shatz/statekit"
	"gopkg.in/yaml.v3"

	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/pkg/chiutil"
)

// stateProvider is the read-only view the per-component routes need.
type stateProvider interface {
	Snapshot() SupervisorSnapshot
	ComponentSnapshot(name string) (ComponentSnapshot, bool)
}

// rejectAPI is the optional write-side: control planes call /components/{name}/reject
// to forcibly demote a version. nil disables the endpoint.
type rejectAPI interface {
	RejectVersion(component, version string) error
}

// httpServer wraps an *http.Server and the chiutil RouteFolder so a control
// plane can discover endpoints by browsing the index.
type httpServer struct {
	addr   string
	server *http.Server
	logger *log.Logger
}

// componentSummaryEntry is one row of the /summary YAML — a flat, easily
// grep-able overview of each component.
type componentSummaryEntry struct {
	Name          string `yaml:"name"`
	Status        string `yaml:"status"`
	Current       string `yaml:"current,omitempty"`
	Stable        string `yaml:"stable,omitempty"`
	PID           int    `yaml:"pid,omitempty"`
	UptimeSeconds int64  `yaml:"uptime_seconds,omitempty"`
	Port          int    `yaml:"port,omitempty"`
	FastCrashes   int    `yaml:"fast_crashes"`
	ExecFailures  int    `yaml:"exec_failures"`
}

type supervisorSummary struct {
	StateDir   string                  `yaml:"state_dir"`
	StartedAt  string                  `yaml:"started_at"`
	Components []componentSummaryEntry `yaml:"components"`
}

func newHTTPServer(addr string, sp stateProvider, ra rejectAPI, bundle *statekitBundle, logger *log.Logger) *httpServer {
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)

	root := chiutil.NewRouteFolder(router, "/").
		ServiceName("go-run supervisor").
		Title("Supervisor").
		Description("Vendor-controlled supervisor: own-state, healthz, components.")

	root.GetDesc("/healthz", "Supervisor liveness probe.", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	// /state and /metrics are served straight from the statekit bundle so the
	// scraper-friendly YAML and the Prometheus text format come from a single
	// source of truth. The YAML is canonical; consumers parse it directly.
	root.GetHandlerDesc("/state", "statekit YAML — aggregated state including scraped components.",
		bundle.registry.StateDisplayYAMLHandler())
	root.GetHandlerDesc("/metrics", "Prometheus exposition (supervisor metrics + scraped component metrics).",
		bundle.registry.PrometheusHandler())

	// /summary is a flat, grep-able YAML overview of each component the
	// supervisor manages. Complementary to /state: same data condensed.
	root.GetDesc("/summary", "YAML summary of each managed component (current, stable, pid, uptime, counters).",
		func(w http.ResponseWriter, _ *http.Request) {
			writeYAML(w, buildSummary(sp.Snapshot()))
		})

	components := root.WildcardFolder("components", "name", func(r chi.Router) {
		// /info: supervisor-side bookkeeping (version triple, pid, counters,
		// monitor port/url paths). Renamed from /state to avoid confusion
		// with the statekit state document at /components/{name}/state.
		r.Get("/info", func(w http.ResponseWriter, r *http.Request) {
			name := chi.URLParam(r, "name")
			snap, ok := sp.ComponentSnapshot(name)
			if !ok {
				http.Error(w, "no such component", http.StatusNotFound)
				return
			}
			writeYAML(w, snap)
		})

		// /state: the statekit state document filtered to entries tagged
		// scraped_from this component (supervisorstate, .up, mirrored child
		// states). Renders in the same YAML shape as the root /state so
		// tools can parse it the same way. Honours ?format=verbose|short.
		r.Get("/state", func(w http.ResponseWriter, r *http.Request) {
			name := chi.URLParam(r, "name")
			if _, ok := sp.ComponentSnapshot(name); !ok {
				http.Error(w, "no such component", http.StatusNotFound)
				return
			}
			doc, err := statekit.ApplyDisplayFormat(componentStateDocument(bundle, name), r.URL.Query().Get("format"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeYAML(w, doc)
		})

		if ra != nil {
			r.Post("/reject", func(w http.ResponseWriter, r *http.Request) {
				name := chi.URLParam(r, "name")
				version := r.URL.Query().Get("version")
				if version == "" {
					http.Error(w, "missing ?version=", http.StatusBadRequest)
					return
				}
				if err := ra.RejectVersion(name, version); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
		}
	}).Title("Components").Description("Per-component info + scraped state view.")

	for _, c := range sp.Snapshot().Components {

		desc := "Component " + c.Name
		if c.Description != "" {
			desc = c.Description
		}
		components.Add(c.Name, desc)
	}

	return &httpServer{
		addr:   addr,
		server: &http.Server{Addr: addr, Handler: router, ReadHeaderTimeout: 5 * time.Second},
		logger: logger,
	}
}

// componentStateDocument builds a statekit StateDisplayDocument scoped to the
// states associated with one component. Same label_path as the supervisor's
// own /state so tools parsing one can parse the other.
func componentStateDocument(bundle *statekitBundle, component string) statekit.StateDisplayDocument {
	full := bundle.registry.StateDisplay()
	filtered := make([]statekit.Snapshot, 0, len(full.States))
	for _, s := range full.States {
		if s.ScrapedFrom == component {
			filtered = append(filtered, s)
		}
	}
	full.States = filtered
	return full
}

func buildSummary(snap SupervisorSnapshot) supervisorSummary {
	out := supervisorSummary{
		StateDir:   snap.StateDir,
		StartedAt:  snap.StartedAt,
		Components: make([]componentSummaryEntry, 0, len(snap.Components)),
	}
	for _, c := range snap.Components {
		out.Components = append(out.Components, componentSummaryEntry{
			Name:          c.Name,
			Status:        c.Status,
			Current:       c.Current,
			Stable:        c.Stable,
			PID:           c.ChildPID,
			UptimeSeconds: c.UptimeSeconds,
			Port:          c.Port,
			FastCrashes:   c.FastCrashes,
			ExecFailures:  c.ExecFailures,
		})
	}
	return out
}

// Start launches ListenAndServe in a background goroutine. Errors land on
// errCh; the caller is expected to read at most one value.
func (this *httpServer) Start(errCh chan<- error) {
	go func() {
		this.logger.Status("HTTP listening on %s", this.addr)
		err := this.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
}

// Shutdown gracefully stops the HTTP server with a short timeout.
func (this *httpServer) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return this.server.Shutdown(shutdownCtx)
}

func writeYAML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	_ = enc.Encode(v)
	_ = enc.Close()
}
