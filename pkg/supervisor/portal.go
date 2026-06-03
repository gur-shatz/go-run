package supervisor

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/gur-shatz/go-run/internal/log"
)

//go:embed portal.html
var portalTemplateHTML string

var portalTemplates = template.Must(template.New("portal").Parse(portalTemplateHTML))

// portalMarkdown renders component README files. The Table extension adds
// GitHub-style pipe tables (the rest is CommonMark). Raw HTML in the markdown
// is dropped (goldmark's safe default — WithUnsafe is not set), which is fine
// for operator-authored docs and avoids injecting arbitrary HTML.
var portalMarkdown = goldmark.New(goldmark.WithExtensions(extension.Table))

// portal serves the supervisor's front-door UI: a grid of component cards at
// "/" that drills down into a per-component page at "/components/<name>/".
// It is the user-facing counterpart to the /backoffice control surface.
type portal struct {
	sp            stateProvider
	controls      controlAPI
	configs       map[string]ComponentConfig
	healthEnabled bool // observer role on -> show links to the /health console
	logger        *log.Logger
}

func newPortal(sp stateProvider, controls controlAPI, components []ComponentConfig, healthEnabled bool, logger *log.Logger) *portal {
	m := make(map[string]ComponentConfig, len(components))
	for _, c := range components {
		m[c.Name] = c
	}
	return &portal{sp: sp, controls: controls, configs: m, healthEnabled: healthEnabled, logger: logger}
}

// mount wires the portal routes on the root router. Links inside the rendered
// pages are relative to a per-page RootRel ("" at "/", "../../" on a component
// page), so the whole portal survives being served behind a path prefix.
func (this *portal) mount(router chi.Router) {
	router.Get("/", this.home)
	router.Get("/components/{name}/", this.component)
	if this.controls != nil {
		router.Post("/components/{name}/start", this.control(this.controls.StartComponent))
		router.Post("/components/{name}/stop", this.control(this.controls.StopComponent))
		router.Post("/components/{name}/restart", this.control(this.controls.RestartComponent))
	}

	// Bare /components/<name> -> /components/<name>/ (relative, proxy-safe).
	router.Get("/components/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", chi.URLParam(r, "name")+"/")
		w.WriteHeader(http.StatusMovedPermanently)
	})
	// /components and /components/ are not pages of their own — send them home.
	redirectHome := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "../")
		w.WriteHeader(http.StatusFound)
	}
	router.Get("/components", redirectHome)
	router.Get("/components/", redirectHome)
}

func (this *portal) control(fn func(context.Context, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := fn(ctx, name); err != nil {
			status := http.StatusInternalServerError
			if strings.HasPrefix(err.Error(), "no such component:") {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		redirect := safeNext(r.FormValue("next"))
		w.Header().Set("Location", redirect)
		w.WriteHeader(http.StatusSeeOther)
	}
}

// portalComponent is one card / page header for a managed component.
type portalComponent struct {
	Name        string
	Description string
	// GlobalState (pass/warn/fail/down, from the component's /healthz) is the
	// primary badge. UpdateStatus (live / pinned to ...) is the second signal.
	GlobalState  string
	UpdateStatus string
	Status       string // statekit lifecycle status (process view), shown as detail
	Current      string
	Stable       string
	Port         int
	HasReadme    bool

	// Health detail, pre-formatted for display.
	Running      bool
	CanStart     bool
	CanStop      bool
	CanRestart   bool
	Uptime       string // "2h 5m" / "not running"
	RunCount     int64
	LastUpgrade  string // "5m ago" / "—"
	ChildPID     int
	FastCrashes  int
	ExecFailures int
}

type portalHomeView struct {
	Title         string
	StartedAt     string
	RootRel       string
	HealthEnabled bool
	Components    []portalComponent
}

type portalComponentView struct {
	portalComponent
	RootRel          string
	HealthEnabled    bool
	ReadmeHTML       template.HTML
	ReadmeConfigured bool
	ReadmeMissing    bool
	URLs             MonitorURLs
}

func (this *portal) card(c ComponentSnapshot) portalComponent {
	cfg := this.configs[c.Name]
	desc := c.Description
	if desc == "" {
		desc = cfg.Description
	}
	return portalComponent{
		Name:         c.Name,
		Description:  desc,
		GlobalState:  c.GlobalState,
		UpdateStatus: c.UpdateStatus,
		Status:       c.Status,
		Current:      c.Current,
		Stable:       c.Stable,
		Port:         c.Port,
		HasReadme:    cfg.Readme != "",
		Running:      c.ChildPID != 0,
		CanStart:     c.ChildPID == 0,
		CanStop:      c.ChildPID != 0,
		CanRestart:   c.Current != "",
		Uptime:       fmtUptime(c.UptimeSeconds),
		RunCount:     c.RunCount,
		LastUpgrade:  fmtAgo(c.LastUpgrade),
		ChildPID:     c.ChildPID,
		FastCrashes:  c.FastCrashes,
		ExecFailures: c.ExecFailures,
	}
}

// fmtUptime renders a child uptime in seconds as a compact "2h 5m" string.
func fmtUptime(secs int64) string {
	if secs <= 0 {
		return "not running"
	}
	return humanDuration(time.Duration(secs) * time.Second)
}

// fmtAgo renders an RFC3339 timestamp as "5m ago", or "—" when absent.
func fmtAgo(rfc3339 string) string {
	if rfc3339 == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	return humanDuration(d) + " ago"
}

// humanDuration formats a duration to two significant units at most.
func humanDuration(d time.Duration) string {
	days := int(d / (24 * time.Hour))
	h := int((d % (24 * time.Hour)) / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	s := int((d % time.Minute) / time.Second)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func (this *portal) home(w http.ResponseWriter, _ *http.Request) {
	snap := this.sp.Snapshot()
	view := portalHomeView{Title: "Supervisor", StartedAt: snap.StartedAt, HealthEnabled: this.healthEnabled}
	for _, c := range snap.Components {
		view.Components = append(view.Components, this.card(c))
	}
	this.render(w, "home", view)
}

func (this *portal) component(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	snap, ok := this.sp.ComponentSnapshot(name)
	if !ok {
		http.Error(w, "no such component: "+name, http.StatusNotFound)
		return
	}
	cfg := this.configs[name]

	view := portalComponentView{
		portalComponent:  this.card(snap),
		RootRel:          "../../",
		HealthEnabled:    this.healthEnabled,
		ReadmeConfigured: cfg.Readme != "",
		URLs:             snap.MonitorURLs,
	}
	if cfg.Readme != "" {
		if md, err := os.ReadFile(cfg.Readme); err != nil {
			view.ReadmeMissing = true
			this.logger.Warn("read readme for %s (%s): %v", name, cfg.Readme, err)
		} else {
			var buf bytes.Buffer
			if err := portalMarkdown.Convert(md, &buf); err != nil {
				view.ReadmeMissing = true
				this.logger.Warn("render readme for %s: %v", name, err)
			} else {
				view.ReadmeHTML = template.HTML(buf.String())
			}
		}
	}
	this.render(w, "component", view)
}

func (this *portal) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := portalTemplates.ExecuteTemplate(w, name, data); err != nil {
		this.logger.Error("render portal %s: %v", name, err)
	}
}
