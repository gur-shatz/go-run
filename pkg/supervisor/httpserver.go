package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strings"
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

type supervisorEnvEntry struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type supervisorEnv struct {
	Env []supervisorEnvEntry `yaml:"env"`
}

// backofficePrefix is the mount point for the supervisor's own control
// surface. Everything the operator browses (state, metrics, summary,
// component info, logs) lives under here so the bare public port can later
// be handed to a child component without colliding with control routes.
const backofficePrefix = "/backoffice"

func newHTTPServer(addr string, sp stateProvider, ra rejectAPI, paths Paths, bundle *statekitBundle, componentCfgs []ComponentConfig, obs *observer, build BuildInfo, auth BasicAuthConfig, logger *log.Logger) *httpServer {
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)

	// Optional login gate. Registered before any route so it covers the entire
	// surface — portal, proxy, /health, and the whole /backoffice tree (the
	// healthz probe included). Unauthenticated requests are bounced to a
	// /login form; the /login and /logout endpoints stay open.
	if auth.Enabled {
		gate := newAuthGate(auth)
		router.Use(gate.middleware)
		router.Get("/login", gate.loginPage)
		router.Post("/login", gate.loginSubmit)
		router.Get("/logout", gate.logout)
	}

	// keep the root router free for supervisor specific routes.

	// Portal: the user-facing home page (component cards) at "/" and a
	// per-component page at "/components/<name>/".
	newPortal(sp, componentCfgs, obs != nil, logger).mount(router)

	// /proxy/<component>/* reverse-proxies to the component's own port.
	mountComponentProxy(router, sp, logger)

	// /health: optional health-aggregation console (statekit storage).
	if obs != nil {
		obs.mount(router)
	}

	// The bare root stays a thin index whose only job is to point operators
	// at /backoffice. The control surface itself is a subfolder.
	bo := chiutil.NewRouteFolder(router, backofficePrefix).
		ServiceName("Backoffice").
		Title("Supervisor").
		Description("Vendor-controlled supervisor. " + buildDescription(build) + " Control surface lives under /backoffice.")

	bo.GetDesc("/healthz", "Supervisor liveness probe.", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	// /version: build metadata (version, branch, commit, build date).
	bo.GetDesc("/version", "Supervisor build info (version, branch, commit, date).",
		func(w http.ResponseWriter, _ *http.Request) {
			writeYAML(w, build)
		})

	// /state and /metrics are served straight from the statekit bundle so the
	// scraper-friendly YAML and the Prometheus text format come from a single
	// source of truth. The YAML is canonical; consumers parse it directly.
	bo.GetHandlerDesc("/state", "statekit YAML — aggregated state including scraped components.",
		bundle.registry.StateDisplayYAMLHandler())
	bo.GetHandlerDesc("/metrics", "Prometheus exposition (supervisor metrics + scraped component metrics).",
		bundle.registry.PrometheusHandler())

	// /summary is a flat, grep-able YAML overview of each component the
	// supervisor manages. Complementary to /state: same data condensed.
	bo.GetDesc("/summary", "YAML summary of each managed component (current, stable, pid, uptime, counters).",
		func(w http.ResponseWriter, _ *http.Request) {
			writeYAML(w, buildSummary(sp.Snapshot()))
		})

	bo.GetDesc("/env", "YAML process environment. Variables with SECRET, PASSWORD, or KEY in the name are redacted.",
		func(w http.ResponseWriter, _ *http.Request) {
			writeYAML(w, buildEnv(os.Environ()))
		})

	components := bo.WildcardFolder("components", "name", func(r chi.Router) {
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

		// /current_version_logs: shortcut into the per-component log tree
		// scoped to whatever current.txt names right now. 302s on every
		// request so the link always tracks the live current — operators
		// don't have to know the version string to land on its logs.
		r.Get("/current_version_logs", func(w http.ResponseWriter, r *http.Request) {
			name := chi.URLParam(r, "name")
			current, err := paths.Component(name).ReadCurrent()
			if err != nil || current == "" {
				http.Error(w, "no current version on disk yet for "+name, http.StatusNotFound)
				return
			}
			// Relative redirect: "../../" climbs from components/<name>/ back to
			// the backoffice root, then descends into the logs tree. Setting
			// Location directly (rather than http.Redirect, which would resolve
			// it to an absolute path) keeps it correct behind a path-stripping
			// reverse proxy, where an absolute /backoffice/... would drop the
			// proxy prefix and miss.
			w.Header().Set("Location", "../../logs/"+name+"/"+current+"/")
			w.WriteHeader(http.StatusFound)
		})
	}).Title("Components").Description("Per-component info + scraped state view.")

	// Render the shortcut as an external link so the index UI opens it in a
	// new tab — operators tailing logs usually want the source page (the
	// component dashboard) to stay put.
	components.MarkInstanceExternal("current_version_logs")

	// Per-component log trees, each mounted as its own browseable static
	// folder. fsRoot is captured at construction so each handler "knows"
	// which component it serves without per-request URL-param lookup. The
	// directory is pre-created so the index UI doesn't 404 before the
	// child has launched for the first time.
	logsRoot := bo.Folder("logs").
		Title("Component logs").
		Description("Per-component stdout / stderr + child-written application logs, browseable as static files.")
	for _, c := range sp.Snapshot().Components {
		desc := "Component " + c.Name
		if c.Description != "" {
			desc = c.Description
		}
		components.Add(c.Name, desc)

		fsRoot := paths.LogsForComponent(c.Name)
		_ = os.MkdirAll(fsRoot, 0o755)
		logsRoot.StaticFilesFolder(c.Name, fsRoot).
			Title("Logs for " + c.Name).
			Description("state_dir/logs/" + c.Name + "/")
	}

	return &httpServer{
		addr:   addr,
		server: &http.Server{Addr: addr, Handler: router, ReadHeaderTimeout: 5 * time.Second},
		logger: logger,
	}
}

// mountComponentProxy wires /proxy/<component>/* on the root router to the
// component's own HTTP port. The port is the supervisor's first-class handle
// on a component: each one binds a well-known port in the shared pod network,
// so the supervisor reverse-proxies to 127.0.0.1:<port>.
//
// The /proxy/<component> prefix is stripped before forwarding, so the child
// receives requests rooted at "/". chiutil-based children rebuild the prefix
// from the browser URL (and X-Forwarded-Prefix is set for any that read it),
// so their links and redirects stay correct under the proxy.
func mountComponentProxy(router chi.Router, sp stateProvider, logger *log.Logger) {
	forward := func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		snap, ok := sp.ComponentSnapshot(name)
		if !ok {
			http.Error(w, "no such component: "+name, http.StatusNotFound)
			return
		}
		if snap.Port <= 0 {
			http.Error(w, "component "+name+" has no port", http.StatusServiceUnavailable)
			return
		}

		target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", snap.Port)}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
			logger.Warn("proxy %s -> %s failed: %v", name, target.Host, err)
			http.Error(w, "component "+name+" unreachable: "+err.Error(), http.StatusBadGateway)
		}

		// Strip the /proxy/<name> mount prefix: the child is rooted at "/".
		r.URL.Path = "/" + chi.URLParam(r, "*")
		r.URL.RawPath = ""
		r.Header.Set("X-Forwarded-Prefix", "/proxy/"+name)
		proxy.ServeHTTP(w, r)
	}

	// All methods (GET, POST, websockets, ...) forward to the child.
	router.Handle("/proxy/{name}/*", http.HandlerFunc(forward))

	// Bare /proxy/<name> -> /proxy/<name>/ so the child's relative links
	// resolve. Relative Location keeps it correct behind an outer proxy.
	router.Get("/proxy/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", chi.URLParam(r, "name")+"/")
		w.WriteHeader(http.StatusMovedPermanently)
	})
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

// buildDescription renders the build metadata into a short sentence for the
// backoffice index, or "" when no build info was injected (dev builds).
func buildDescription(b BuildInfo) string {
	if b.Version == "" && b.Branch == "" {
		return ""
	}
	desc := "Build"
	if b.Version != "" {
		desc = "Version " + b.Version
	}
	if b.Branch != "" {
		desc += " (branch " + b.Branch + ")"
	}
	return desc + "."
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

func buildEnv(environ []string) supervisorEnv {
	out := supervisorEnv{Env: make([]supervisorEnvEntry, 0, len(environ))}
	for _, item := range environ {
		name, value, _ := strings.Cut(item, "=")
		if isSensitiveEnvName(name) {
			value = "[REDACTED]"
		}
		out.Env = append(out.Env, supervisorEnvEntry{Name: name, Value: value})
	}
	sort.Slice(out.Env, func(i, j int) bool {
		return out.Env[i].Name < out.Env[j].Name
	})
	return out
}

func isSensitiveEnvName(name string) bool {
	upper := strings.ToUpper(name)
	return strings.Contains(upper, "SECRET") ||
		strings.Contains(upper, "PASSWORD") ||
		strings.Contains(upper, "KEY")
}

// Start launches ListenAndServe in a background goroutine. Errors land on
// errCh; the caller is expected to read at most one value.
func (this *httpServer) Start(errCh chan<- error) {
	go func() {
		this.logger.Status("HTTP listening on http://%s", this.addr)
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
