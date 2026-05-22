package supervisor

import (
	"sort"
	"time"

	"github.com/gur-shatz/statekit"
)

// statekitBundle is the per-supervisor view of state and metrics expressed in
// statekit terms. For each component the bundle owns an AggregateState named
// "<component>.supervisorstate" whose children carry the supervisor's local
// view across distinct concerns:
//
//   - "uptime" (Important): the lifecycle — running, restarting, halted. Carries
//     the per-state metrics (runcount, uptime_seconds, fast_crashes,
//     exec_failures) so a glance at the YAML answers "what's the child doing?"
//   - "update" (Informational): the update flow — last poll result, current
//     target version. Informational so a poll failure caps the aggregate at
//     warn (not fail). Failures here mean "we couldn't reach the vendor", not
//     "the workload is broken."
//
// Scraped child data still arrives separately via the statekit/scraper —
// liveness probes (`<component>.up`), mirrored top-level states, and child
// metrics. The bundle owns only what the supervisor itself produces.
type statekitBundle struct {
	registry *statekit.Registry

	// Per-component state handles. The Component lifecycle pushes into
	// `lifecycle`; the Updater pushes into `update`.
	components map[string]*componentStates

	// Per-component gauges (aggregate /metrics view). fast_crashes and
	// exec_failures are gauges, not counters, because they reset on
	// stability_time — Prometheus counters would emit a confusing reset
	// rather than the intended "supervisor recovered" signal.
	fastCrashes  *statekit.GaugeVec
	execFailures *statekit.GaugeVec
	runCount     *statekit.CounterVec

	// Gauges labelled by component. uptimeSeconds reflects the current
	// child's uptime; monitorPort is informational so a scrape can recover
	// it without re-reading config.
	uptimeSeconds *statekit.GaugeVec
	monitorPort   *statekit.GaugeVec

	// Per-component scalar metrics attached to the "uptime" leaf via
	// AddMetric — they appear inside the leaf's "metrics:" block in /state.
	perComponentRunCount     map[string]*statekit.Counter
	perComponentUptime       map[string]*statekit.Gauge
	perComponentFastCrashes  map[string]*statekit.Gauge
	perComponentExecFailures map[string]*statekit.Gauge
}

// componentStates holds the two ManualStates that feed one component's
// supervisorstate aggregate.
type componentStates struct {
	lifecycle *statekit.ManualState // the "uptime" leaf
	update    *statekit.ManualState // the "update" leaf
}

func newStatekitBundle(cfg Config) *statekitBundle {
	// Sort label keys so the registry's label_path is deterministic across
	// restarts — useful when a fleet aggregator dedupes by label_path.
	keys := make([]string, 0, len(cfg.Supervisor.MetricLabels))
	for k := range cfg.Supervisor.MetricLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	opts := make([]statekit.RegistryOption, 0, len(keys))
	for _, k := range keys {
		opts = append(opts, statekit.WithLabel(k, cfg.Supervisor.MetricLabels[k]))
	}
	reg := statekit.NewRegistry(opts...)

	b := &statekitBundle{
		registry:                 reg,
		components:               make(map[string]*componentStates, len(cfg.Components)),
		fastCrashes:              statekit.NewGaugeVec("component_fast_crashes", "Fast crashes since the last stability_time reset.", "component"),
		execFailures:             statekit.NewGaugeVec("component_exec_failures", "Supervisor exec failures since the last stability_time reset.", "component"),
		runCount:                 statekit.NewCounterVec("component_runcount_total", "Child launches since the supervisor started.", "component"),
		uptimeSeconds:            statekit.NewGaugeVec("component_uptime_seconds", "Current child uptime in seconds.", "component"),
		monitorPort:              statekit.NewGaugeVec("component_monitor_port", "Configured monitor port for the component.", "component"),
		perComponentRunCount:     make(map[string]*statekit.Counter, len(cfg.Components)),
		perComponentUptime:       make(map[string]*statekit.Gauge, len(cfg.Components)),
		perComponentFastCrashes:  make(map[string]*statekit.Gauge, len(cfg.Components)),
		perComponentExecFailures: make(map[string]*statekit.Gauge, len(cfg.Components)),
	}
	_ = reg.RegisterCollectors(b.fastCrashes, b.execFailures, b.runCount, b.uptimeSeconds, b.monitorPort)

	for _, c := range cfg.Components {
		// Lifecycle leaf — Important. Mirrors the supervisor's local view of
		// the child process and carries the per-state metrics.
		lifecycle := statekit.NewManualState("uptime")
		lifecycle.Down("not started", nil)

		runCount := statekit.NewCounter("runcount", "Times the supervisor has launched this component since it started.")
		uptimeMetric := statekit.NewGauge("uptime_seconds", "Current child uptime in seconds (0 if no child is running).")
		fast := statekit.NewGauge("fast_crashes", "Fast crashes since the last stability_time reset.")
		execFail := statekit.NewGauge("exec_failures", "Supervisor exec failures since the last stability_time reset.")
		lifecycle.AddMetric(runCount, uptimeMetric, fast, execFail)

		// Update leaf — Informational. The update flow's health (last poll
		// success/failure, current target version) shouldn't push the
		// aggregate past warn even when the remote is unreachable.
		update := statekit.NewManualState("update", statekit.WithImportance(statekit.Informational))
		update.Pass("waiting for first poll", nil)

		// The aggregate is what the registry actually shows under
		// <name>.supervisorstate. Children are wrapped in taggedState so each
		// row in /state and each Prometheus state_level sample carries
		// scraped_from: <component>.
		agg := statekit.NewStateAggregator(c.Name + ".supervisorstate")
		agg.AddCheck(&taggedState{underlying: lifecycle, scrapedFrom: c.Name})
		agg.AddInformationalCheck(&taggedState{underlying: update, scrapedFrom: c.Name})

		b.components[c.Name] = &componentStates{lifecycle: lifecycle, update: update}
		b.perComponentRunCount[c.Name] = runCount
		b.perComponentUptime[c.Name] = uptimeMetric
		b.perComponentFastCrashes[c.Name] = fast
		b.perComponentExecFailures[c.Name] = execFail

		_ = reg.Register(&taggedState{underlying: agg, scrapedFrom: c.Name})

		b.monitorPort.WithLabelValues(c.Name).Set(int64(c.Port))
		// Materialise the lazy GaugeVec / CounterVec entries so a zero
		// baseline is visible in /metrics from boot.
		b.fastCrashes.WithLabelValues(c.Name).Set(0)
		b.execFailures.WithLabelValues(c.Name).Set(0)
		b.uptimeSeconds.WithLabelValues(c.Name).Set(0)
		b.runCount.WithLabelValues(c.Name)
	}
	return b
}

// taggedState wraps a statekit.State and stamps ScrapedFrom on every snapshot.
// First producer wins for ScrapedFrom; ScrapePath chains. Children of an
// aggregate are wrapped individually so each row carries the tag.
type taggedState struct {
	underlying  statekit.State
	scrapedFrom string
}

func (this *taggedState) Name() string { return this.underlying.Name() }

func (this *taggedState) Snapshot() statekit.Snapshot {
	snap := this.underlying.Snapshot()
	if snap.ScrapedFrom == "" {
		snap.ScrapedFrom = this.scrapedFrom
	}
	if snap.ScrapePath == "" {
		snap.ScrapePath = this.scrapedFrom
	} else {
		snap.ScrapePath = this.scrapedFrom + " > " + snap.ScrapePath
	}
	return snap
}

// markPass transitions the lifecycle leaf to pass with reason. Safe with a
// nil bundle (no-op).
func (this *statekitBundle) markPass(name, reason string) {
	if cs, ok := this.components[name]; ok {
		cs.lifecycle.Pass(reason, nil)
	}
}

// markWarn transitions the lifecycle leaf to warn with reason.
func (this *statekitBundle) markWarn(name, reason string) {
	if cs, ok := this.components[name]; ok {
		cs.lifecycle.Warn(reason, nil)
	}
}

// markFail transitions the lifecycle leaf to fail with reason.
func (this *statekitBundle) markFail(name, reason string) {
	if cs, ok := this.components[name]; ok {
		cs.lifecycle.Fail(reason, nil)
	}
}

// markDown transitions the lifecycle leaf to down with reason.
func (this *statekitBundle) markDown(name, reason string) {
	if cs, ok := this.components[name]; ok {
		cs.lifecycle.Down(reason, nil)
	}
}

// lifecycleSnapshot returns the current snapshot of the lifecycle leaf, used
// by Component.Snapshot() to surface the statekit status in /info and
// /summary. Returns the zero Snapshot if the component isn't known.
func (this *statekitBundle) lifecycleSnapshot(name string) statekit.Snapshot {
	if cs, ok := this.components[name]; ok {
		return cs.lifecycle.Snapshot()
	}
	return statekit.Snapshot{}
}

// observeUpdateOK marks the update sub-state as healthy.
func (this *statekitBundle) observeUpdateOK(name, reason string) {
	if cs, ok := this.components[name]; ok {
		cs.update.Pass(reason, nil)
	}
}

// observeUpdateWarn marks the update sub-state as degraded (e.g. resolved a
// version but waiting on download to complete).
func (this *statekitBundle) observeUpdateWarn(name, reason string) {
	if cs, ok := this.components[name]; ok {
		cs.update.Warn(reason, nil)
	}
}

// observeUpdateError marks the update sub-state as failed. Because the leaf
// is Informational the aggregate caps the contribution at warn — failures
// here flag a problem but don't trip the whole supervisorstate to fail.
func (this *statekitBundle) observeUpdateError(name string, err error) {
	if cs, ok := this.components[name]; ok {
		cs.update.Fail(err.Error(), nil)
	}
}

// observeFastCrash bumps the per-component fast_crashes gauge by one (both
// the registry-level GaugeVec for /metrics and the state-attached scalar
// Gauge visible inside /state).
func (this *statekitBundle) observeFastCrash(name string) {
	this.fastCrashes.WithLabelValues(name).Add(1)
	if g, ok := this.perComponentFastCrashes[name]; ok {
		g.Add(1)
	}
}

// observeExecFailure bumps the per-component exec_failures gauge by one.
func (this *statekitBundle) observeExecFailure(name string) {
	this.execFailures.WithLabelValues(name).Add(1)
	if g, ok := this.perComponentExecFailures[name]; ok {
		g.Add(1)
	}
}

// observeStability is called when the component achieves stability_time of
// continuous uptime. Both crash gauges return to zero — including for a
// rejected version, which is how the supervisor recovers without operator
// intervention if a "bad" version turns out to run cleanly after all.
func (this *statekitBundle) observeStability(name string) {
	this.fastCrashes.WithLabelValues(name).Set(0)
	this.execFailures.WithLabelValues(name).Set(0)
	if g, ok := this.perComponentFastCrashes[name]; ok {
		g.Set(0)
	}
	if g, ok := this.perComponentExecFailures[name]; ok {
		g.Set(0)
	}
}

// observeLaunch records that a child has just been started. Drives both the
// per-state "runcount" metric (visible inline in /state) and the labelled
// component_runcount_total CounterVec (visible in /metrics).
func (this *statekitBundle) observeLaunch(name string) {
	if c, ok := this.perComponentRunCount[name]; ok {
		c.Inc()
	}
	this.runCount.WithLabelValues(name).Inc()
}

// observeUptime sets the component's uptime gauges to the elapsed seconds.
// Updates both the aggregate GaugeVec (for /metrics) and the per-state Gauge
// attached to the uptime leaf (for inline /state visibility).
func (this *statekitBundle) observeUptime(name string, since time.Time) {
	var v int64
	if !since.IsZero() {
		v = int64(time.Since(since).Seconds())
	}
	this.uptimeSeconds.WithLabelValues(name).Set(v)
	if g, ok := this.perComponentUptime[name]; ok {
		g.Set(v)
	}
}
