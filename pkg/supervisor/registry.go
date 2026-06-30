package supervisor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gur-shatz/statekit"
)

// incidentTypeCrash marks component-scoped crash episodes. Unlike the
// statekit global types (deployment, rollback) it renders as a per-target
// range on the health console timeline, not a system-wide band.
const incidentTypeCrash = "crash"

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

	// escalations turns lifecycle events (deploy, crash, rollback) into
	// statekit incidents, exported at /backoffice/escalations and mirrored
	// into the observer store. openIncidents tracks at most one live
	// incident per component — a crash loop is one incident accumulating
	// events, not one incident per exit.
	escalations   *statekit.Escalations
	incidentMu    sync.Mutex
	openIncidents map[string]*statekit.Escalation

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

	// Per-component memory gauges (current/derived budgets/share), plus
	// pod-level scalars and the resolved mode. Populated by the memory monitor
	// each sample; absent entirely when the subsystem is disabled.
	memoryCurrent *statekit.GaugeVec
	memoryHigh    *statekit.GaugeVec
	memoryLimit   *statekit.GaugeVec
	memoryMode    *statekit.GaugeVec
	podLimit      *statekit.Gauge
	machineTotal  *statekit.Gauge
	cgroupLimit   *statekit.Gauge
	podCurrent    *statekit.Gauge
	workloadCur   *statekit.Gauge
	memorySampleT *statekit.Gauge

	// Per-component scalar metrics attached to the "uptime" leaf via
	// AddMetric — they appear inside the leaf's "metrics:" block in /state.
	perComponentRunCount     map[string]*statekit.Counter
	perComponentUptime       map[string]*statekit.Gauge
	perComponentFastCrashes  map[string]*statekit.Gauge
	perComponentExecFailures map[string]*statekit.Gauge
}

// componentStates holds the ManualStates that feed one component's
// supervisorstate aggregate.
type componentStates struct {
	lifecycle *statekit.ManualState // the "uptime" leaf
	update    *statekit.ManualState // the "update" leaf
	memory    *statekit.ManualState // the "memory" leaf (nil when subsystem off)
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
		registry: reg,
		// Generous budget: nothing acks incidents unless an upstream
		// statekit scraper consumes /backoffice/escalations, so closed
		// incidents linger until the TTL recycles them.
		escalations: statekit.NewEscalations(statekit.WithEscalationPolicy(statekit.EscalationPolicy{
			MaxUnacknowledged: 100,
			TTL:               24 * time.Hour,
		})),
		openIncidents:            make(map[string]*statekit.Escalation, len(cfg.Components)),
		components:               make(map[string]*componentStates, len(cfg.Components)),
		fastCrashes:              statekit.NewGaugeVec("component_fast_crashes", "Fast crashes since the last stability_time reset.", "component"),
		execFailures:             statekit.NewGaugeVec("component_exec_failures", "Supervisor exec failures since the last stability_time reset.", "component"),
		runCount:                 statekit.NewCounterVec("component_runcount_total", "Child launches since the supervisor started.", "component"),
		uptimeSeconds:            statekit.NewGaugeVec("component_uptime_seconds", "Current child uptime in seconds.", "component"),
		monitorPort:              statekit.NewGaugeVec("component_monitor_port", "Configured monitor port for the component.", "component"),
		memoryCurrent:            statekit.NewGaugeVec("component_memory_current_bytes", "Current memory usage of the component (process RSS in tracking mode).", "component"),
		memoryHigh:               statekit.NewGaugeVec("component_memory_high_bytes", "Derived soft memory budget for the component (0 if unbudgeted).", "component"),
		memoryLimit:              statekit.NewGaugeVec("component_memory_limit_bytes", "Derived hard memory budget for the component (0 if unbudgeted).", "component"),
		memoryMode:               statekit.NewGaugeVec("memory_mode", "Resolved memory subsystem mode; 1 for the active mode.", "mode"),
		podLimit:                 statekit.NewGauge("pod_memory_global_limit_bytes", "Resolved pod memory limit in bytes (0 if unresolved)."),
		machineTotal:             statekit.NewGauge("machine_memory_total_bytes", "Host total physical RAM in bytes (context only; not the pod budget)."),
		cgroupLimit:              statekit.NewGauge("cgroup_memory_limit_bytes", "Container cgroup memory.max in bytes (0 if none/unknown)."),
		podCurrent:               statekit.NewGauge("pod_memory_current_bytes", "Container cgroup current memory in bytes (summed RSS in host mode)."),
		workloadCur:              statekit.NewGauge("workload_memory_current_bytes", "Summed current memory across tracked components in bytes."),
		memorySampleT:            statekit.NewGauge("memory_last_sample_timestamp_seconds", "Unix time of the last memory sample."),
		perComponentRunCount:     make(map[string]*statekit.Counter, len(cfg.Components)),
		perComponentUptime:       make(map[string]*statekit.Gauge, len(cfg.Components)),
		perComponentFastCrashes:  make(map[string]*statekit.Gauge, len(cfg.Components)),
		perComponentExecFailures: make(map[string]*statekit.Gauge, len(cfg.Components)),
	}
	_ = reg.RegisterCollectors(b.fastCrashes, b.execFailures, b.runCount, b.uptimeSeconds, b.monitorPort,
		b.memoryCurrent, b.memoryHigh, b.memoryLimit, b.memoryMode, b.podLimit, b.machineTotal, b.cgroupLimit, b.podCurrent, b.workloadCur, b.memorySampleT)
	reg.RegisterEscalations(b.escalations)

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

		// Memory leaf — Important, only when the subsystem is enabled. Memory
		// pressure escalates the component: soft -> warn, hard -> fail, so the
		// aggregate (and the portal badge) reflect a component eating memory.
		var memory *statekit.ManualState
		if cfg.Memory.IsEnabled() {
			memory = statekit.NewManualState("memory")
			memory.Pass("no data yet", nil)
		}

		// The aggregate is what the registry actually shows under
		// <name>.supervisorstate. Children are wrapped in taggedState so each
		// row in /state and each Prometheus state_level sample carries
		// scraped_from: <component>.
		agg := statekit.NewStateAggregator(c.Name + ".supervisorstate")
		agg.AddCheck(&taggedState{underlying: lifecycle, scrapedFrom: c.Name})
		agg.AddInformationalCheck(&taggedState{underlying: update, scrapedFrom: c.Name})
		if memory != nil {
			agg.AddCheck(&taggedState{underlying: memory, scrapedFrom: c.Name})
		}

		b.components[c.Name] = &componentStates{lifecycle: lifecycle, update: update, memory: memory}
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

// runCountFor returns how many times the supervisor has launched this
// component since it started (0 if the component isn't known).
func (this *statekitBundle) runCountFor(name string) int64 {
	if c, ok := this.perComponentRunCount[name]; ok {
		return int64(c.Get())
	}
	return 0
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

// updateSnapshot returns the updater leaf snapshot, used by Component.Snapshot
// and the portal to surface rejected/blocked target information.
func (this *statekitBundle) updateSnapshot(name string) statekit.Snapshot {
	if cs, ok := this.components[name]; ok {
		return cs.update.Snapshot()
	}
	return statekit.Snapshot{}
}

func (this *statekitBundle) scrapedLivenessSnapshot(name string) statekit.Snapshot {
	if this == nil || this.registry == nil {
		return statekit.Snapshot{}
	}
	var fallback statekit.Snapshot
	for _, s := range this.registry.StateDisplay().States {
		if s.ScrapedFrom != name {
			continue
		}
		if fallback.Name == "" {
			fallback = s
		}
		if strings.HasSuffix(s.Name, ".responsive") {
			return s
		}
	}
	return fallback
}

// worstStatus returns the favicon-level rollup across every registered state:
// red for fail/down, yellow for warn, green for pass or no states.
func (this *statekitBundle) worstStatus() statekit.Status {
	if this == nil || this.registry == nil {
		return statekit.Pass
	}
	worst := statekit.Pass
	for _, s := range this.registry.StateDisplay().States {
		switch s.Status {
		case statekit.Fail, statekit.Down:
			return s.Status
		case statekit.Warn:
			worst = statekit.Warn
		}
	}
	return worst
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

// incidentDeploy opens a deployment incident when the lifecycle switches a
// running child to a new version. Any incident still open for the component
// (e.g. a crash episode on the old version) is superseded — the deploy ends
// that story and starts a new one. The incident stays open until
// incidentStabilized or incidentRollback resolves it, so the health console
// timeline shows a deployment band spanning switch → outcome.
func (this *statekitBundle) incidentDeploy(name, from, to string) {
	this.closeIncident(name, "superseded by deploy to "+to)
	this.startIncident(name, statekit.EscalationSpec{
		Type:     statekit.EscalationTypeDeployment,
		Title:    name + ": deploy " + from + " → " + to,
		Severity: statekit.Pass,
		Topics:   map[string]any{"component": name, "from": from, "to": to},
	}, "deployment", "switching "+from+" → "+to, nil)
}

// incidentCrash records a child exit or exec failure. If the component
// already has an open incident (a deployment in flight, or an earlier crash
// in the same episode) the event is appended there — a crash loop is one
// incident accumulating events, not one incident per exit. Otherwise a new
// crash incident opens.
func (this *statekitBundle) incidentCrash(name, version, detail string, data map[string]any) {
	if this.appendIncident(name, incidentTypeCrash, detail, data) {
		return
	}
	this.startIncident(name, statekit.EscalationSpec{
		Type:     incidentTypeCrash,
		Title:    name + ": " + version + " crashing",
		Severity: statekit.Fail,
		Topics:   map[string]any{"component": name, "version": version},
	}, incidentTypeCrash, detail, data)
}

// incidentRollback closes the open incident (the crash episode or deployment
// that tripped the bad-version criterion) and opens a rollback incident.
// stable is empty when no fallback version was available.
func (this *statekitBundle) incidentRollback(name, rejected, stable, reason string) {
	this.closeIncident(name, "demoting "+rejected+": "+reason)
	title := name + ": rollback " + rejected + " → " + stable
	if stable == "" {
		title = name + ": rejected " + rejected + ", no stable fallback"
	}
	this.startIncident(name, statekit.EscalationSpec{
		Type:     statekit.EscalationTypeRollback,
		Title:    title,
		Severity: statekit.Warn,
		Topics:   map[string]any{"component": name, "rejected": rejected, "stable": stable},
	}, "rollback", reason, nil)
}

// incidentStabilized resolves the component's open incident once the child
// reaches stability_time of continuous uptime. No-op when nothing is open —
// MaybeStabilize keeps firing on every stability tick.
func (this *statekitBundle) incidentStabilized(name, version string) {
	this.closeIncident(name, version+" stable")
}

// startIncident opens an incident and remembers the handle so later
// lifecycle events append to it. A rejected Start (unacknowledged budget
// exhausted) leaves the component with no open incident.
func (this *statekitBundle) startIncident(name string, spec statekit.EscalationSpec, topic, message string, data map[string]any) {
	inc, ok := this.escalations.Start(context.Background(), spec)
	if !ok {
		return
	}
	inc.AddLog(context.Background(), time.Now(), topic, message, data)
	this.incidentMu.Lock()
	this.openIncidents[name] = inc
	this.incidentMu.Unlock()
}

// appendIncident adds an event to the component's open incident. Reports
// false when no incident is open.
func (this *statekitBundle) appendIncident(name, topic, message string, data map[string]any) bool {
	this.incidentMu.Lock()
	inc := this.openIncidents[name]
	this.incidentMu.Unlock()
	if inc == nil {
		return false
	}
	inc.AddLog(context.Background(), time.Now(), topic, message, data)
	return true
}

// closeIncident closes and forgets the component's open incident. No-op when
// nothing is open.
func (this *statekitBundle) closeIncident(name, reason string) {
	this.incidentMu.Lock()
	inc := this.openIncidents[name]
	delete(this.openIncidents, name)
	this.incidentMu.Unlock()
	inc.Close(context.Background(), reason)
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

// observeMemory records one component's current usage and derived budgets, and
// drives the memory state leaf: soft -> warn, hard -> fail, otherwise pass.
// Called each memory sample; the high/max gauges read 0 in tracking-only modes.
func (this *statekitBundle) observeMemory(name string, current, high, limit int64, state string) {
	this.memoryCurrent.WithLabelValues(name).Set(current)
	this.memoryHigh.WithLabelValues(name).Set(high)
	this.memoryLimit.WithLabelValues(name).Set(limit)

	cs, ok := this.components[name]
	if !ok || cs.memory == nil {
		return
	}
	switch state {
	case memStateSoft:
		cs.memory.Warn(fmt.Sprintf("%s over soft limit %s", humanBytes(current), humanBytes(high)), nil)
	case memStateHard:
		cs.memory.Fail(fmt.Sprintf("%s over hard limit %s", humanBytes(current), humanBytes(limit)), nil)
	case memStateOK:
		cs.memory.Pass(fmt.Sprintf("%s (under soft %s)", humanBytes(current), humanBytes(high)), nil)
	default:
		// Tracking-only: no budget to assess against.
		cs.memory.Pass(fmt.Sprintf("%s (tracking only)", humanBytes(current)), nil)
	}
}

// observePodMemory records the pod-level limit, container current, and workload
// total in bytes.
func (this *statekitBundle) observePodMemory(limit, current, workload int64) {
	this.podLimit.Set(limit)
	this.podCurrent.Set(current)
	this.workloadCur.Set(workload)
}

// observeMemoryMode marks the active mode (the labelled gauge reads 1 for the
// resolved mode). Idempotent: called once at startup.
func (this *statekitBundle) observeMemoryMode(mode MemoryMode) {
	this.memoryMode.WithLabelValues(string(mode)).Set(1)
}

// observeGlobalLimit records the resolved pod limit early, before the first
// sample, so /metrics shows it from boot.
func (this *statekitBundle) observeGlobalLimit(limit int64) {
	this.podLimit.Set(limit)
}

// observeMemoryContext records the static host/cgroup context figures once at
// startup so /metrics shows machine capacity and the cgroup limit from boot.
func (this *statekitBundle) observeMemoryContext(machineTotal, cgroupLimit int64) {
	this.machineTotal.Set(machineTotal)
	this.cgroupLimit.Set(cgroupLimit)
}

// observeMemorySampleTime records the wall-clock time of the latest sample, so
// staleness alerts can fire when sampling stops.
func (this *statekitBundle) observeMemorySampleTime(t time.Time) {
	this.memorySampleT.Set(t.Unix())
}
