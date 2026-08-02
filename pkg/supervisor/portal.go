package supervisor

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"sort"
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
	configs       map[string]portalConfig
	healthEnabled bool // observer role on -> show links to the /health console
	mem           *memoryMonitor
	logger        *log.Logger
}

type portalConfig struct {
	Description string
	Readme      string
}

func newPortal(sp stateProvider, controls controlAPI, components []ComponentConfig, external []ExternalComponentConfig, healthEnabled bool, mem *memoryMonitor, logger *log.Logger) *portal {
	m := make(map[string]portalConfig, len(components)+len(external))
	for _, c := range components {
		m[c.Name] = portalConfig{Description: c.Description, Readme: c.Readme}
	}
	for _, c := range external {
		m[c.Name] = portalConfig{Description: c.Description, Readme: c.Readme}
	}
	return &portal{sp: sp, controls: controls, configs: m, healthEnabled: healthEnabled, mem: mem, logger: logger}
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
	External    bool
	// GlobalState (pass/warn/fail/down, from the component's /healthz) is the
	// primary badge. UpdateStatus (live / pinned to ...) is the second signal.
	GlobalState  string
	DisplayState string
	UpdateStatus string
	UpdateState  string
	UpdateReason string
	Status       string // statekit lifecycle status (process view), shown as run detail
	StatusReason string
	Current      string
	Stable       string
	Port         int
	HasReadme    bool
	ProxyLinks   []portalProxyLink

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

	// Memory, pre-formatted for display. MemoryTracked is false when the
	// subsystem is off / the component has no sample yet, in which case the
	// templates omit every memory element. MemoryBudgeted is true only when a
	// derived hard limit exists (so the "/ max" half and pressure show).
	MemoryTracked   bool
	MemoryBudgeted  bool
	MemoryCurrent   string // "168.0 MiB"
	MemoryHigh      string // soft limit, "" when unbudgeted
	MemoryLimit     string // hard limit, "" when unbudgeted
	MemoryState     string // ok / soft / hard / ""
	MemoryPressure  string // "89%" / ""
	MemoryLastEvent string // "child_exit at 2026-06-30T05:36:55Z" / ""
	MemorySparkline template.HTML
}

type portalProxyLink struct {
	Key string
}

type portalHomeView struct {
	Title         string
	StartedAt     string
	RunningFor    string
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
	pc := portalComponent{
		Name:         c.Name,
		Description:  desc,
		External:     c.External,
		GlobalState:  c.GlobalState,
		DisplayState: portalDisplayState(c.GlobalState, c.Status, c.UpdateState, c.MemoryState),
		UpdateStatus: c.UpdateStatus,
		UpdateState:  c.UpdateState,
		UpdateReason: c.UpdateReason,
		Status:       c.Status,
		StatusReason: c.StatusReason,
		Current:      c.Current,
		Stable:       c.Stable,
		Port:         c.Port,
		HasReadme:    cfg.Readme != "",
		ProxyLinks:   portalProxyLinks(c.ProxyURLs),
		Running:      c.ChildPID != 0,
		CanStart:     !c.External && c.ChildPID == 0,
		CanStop:      !c.External && c.ChildPID != 0,
		CanRestart:   !c.External && c.Current != "",
		Uptime:       fmtUptime(c.UptimeSeconds),
		RunCount:     c.RunCount,
		LastUpgrade:  fmtAgo(c.LastUpgrade),
		ChildPID:     c.ChildPID,
		FastCrashes:  c.FastCrashes,
		ExecFailures: c.ExecFailures,
	}
	pc.fillMemory(c)
	pc.MemoryLastEvent = c.MemoryLastEvent
	return pc
}

// fillMemory pre-formats the memory fields from a snapshot. A component with no
// sample (subsystem off, not running, unsupported platform) stays MemoryTracked
// false so every memory element is omitted.
func (this *portalComponent) fillMemory(c ComponentSnapshot) {
	tracked := c.MemoryCurrentBytes > 0 || c.MemoryState != ""
	if !tracked {
		return
	}
	this.MemoryTracked = true
	this.MemoryCurrent = humanBytes(c.MemoryCurrentBytes)
	this.MemoryState = c.MemoryState
	if c.MemoryLimitBytes > 0 {
		this.MemoryBudgeted = true
		this.MemoryHigh = humanBytes(c.MemoryHighBytes)
		this.MemoryLimit = humanBytes(c.MemoryLimitBytes)
		this.MemoryPressure = fmt.Sprintf("%.0f%%", c.MemoryPressureRatio*100)
	}
}

func portalProxyLinks(proxyURLs map[string]string) []portalProxyLink {
	links := make([]portalProxyLink, 0, len(proxyURLs))
	for key := range proxyURLs {
		links = append(links, portalProxyLink{Key: key})
	}
	sort.Slice(links, func(i, j int) bool { return links[i].Key < links[j].Key })
	return links
}

// portalDisplayState downgrades the visible component badge when the
// supervisor knows updates are blocked/degraded. Updater trouble is capped at
// warn because the update leaf is informational; a bad vendor poll should not
// hide a child-reported fail/down, and should not turn a healthy child red.
func portalDisplayState(global, run, update, memory string) string {
	if global == "" {
		global = "down"
	}
	display := global
	if run != "" && statusRank(display) < statusRank(run) {
		display = run
	}
	if update != "" && update != "pass" && statusRank(display) < statusRank("warn") {
		display = "warn"
	}
	// Memory pressure escalates the badge: soft -> warn, hard -> fail.
	if sev := memStateSeverity(memory); sev != "" && statusRank(display) < statusRank(sev) {
		display = sev
	}
	return display
}

// memStateSeverity maps a memory assessment state to the portal's pass/warn/
// fail vocabulary. ok and tracking-only ("") do not escalate the badge.
func memStateSeverity(state string) string {
	switch state {
	case memStateSoft:
		return "warn"
	case memStateHard:
		return "fail"
	default:
		return ""
	}
}

func statusRank(status string) int {
	switch status {
	case "pass":
		return 0
	case "warn":
		return 1
	case "fail":
		return 2
	case "down":
		return 3
	default:
		return 3
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
	return agoSince(t)
}

// agoSince renders how long ago t was, e.g. "5m ago", collapsing anything
// under a minute to "just now".
func agoSince(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	return humanDuration(d) + " ago"
}

func fmtRunningFor(rfc3339 string) string {
	if rfc3339 == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	return humanDuration(d)
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
	view := portalHomeView{
		Title:         "Supervisor",
		StartedAt:     snap.StartedAt,
		RunningFor:    fmtRunningFor(snap.StartedAt),
		HealthEnabled: this.healthEnabled,
	}
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
	if view.MemoryTracked {
		view.MemorySparkline = memorySparkline(this.mem.componentSeries(name, 0))
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
