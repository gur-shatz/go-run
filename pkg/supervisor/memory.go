package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/gur-shatz/go-run/internal/log"
)

// podMemory is the pod-level view in one sample. CurrentBytes is the container
// cgroup charge when available (the source of truth for a pod-level decision),
// otherwise the summed component current as a host-mode approximation.
type podMemory struct {
	// LimitBytes/LimitSource are the resolved pod budget and which source won
	// (env var or cgroup). MachineTotalBytes and CgroupLimitBytes are shown
	// alongside for context: the whole host's RAM, and the cgroup's own
	// memory.max even when the env var wins the resolved limit.
	LimitBytes           int64   `json:"limit_bytes,omitempty"`
	LimitSource          string  `json:"limit_source,omitempty"`
	MachineTotalBytes    int64   `json:"machine_total_bytes,omitempty"`
	CgroupLimitBytes     int64   `json:"cgroup_limit_bytes,omitempty"`
	CurrentBytes         int64   `json:"current_bytes,omitempty"`
	WorkloadCurrentBytes int64   `json:"workload_current_bytes,omitempty"`
	PSISomeRatio         float64 `json:"psi_some_ratio,omitempty"` // pod-level PSI (cgroup2)
}

// componentMemory is one component's figures in a sample. Under cgroup2
// CurrentBytes is the exact leaf memory.current; in host/cgroup1 modes it falls
// back to process RSS. HighBytes/LimitBytes are the derived budgets. The
// cgroup-only fields (PSS, the anon/file/slab/sock split, the event counters,
// and per-leaf PSI) are omitted when not sampled.
type componentMemory struct {
	Name         string  `json:"name"`
	PID          int     `json:"pid,omitempty"`
	Share        float64 `json:"share,omitempty"`
	CurrentBytes int64   `json:"current_bytes"`
	RSSBytes     int64   `json:"rss_bytes,omitempty"`
	PSSBytes     int64   `json:"pss_bytes,omitempty"`
	HighBytes    int64   `json:"high_bytes,omitempty"`
	LimitBytes   int64   `json:"limit_bytes,omitempty"`
	AnonBytes    int64   `json:"anon_bytes,omitempty"`
	FileBytes    int64   `json:"file_bytes,omitempty"`
	SlabBytes    int64   `json:"slab_bytes,omitempty"`
	SockBytes    int64   `json:"sock_bytes,omitempty"`
	EventsHigh   int64   `json:"events_high,omitempty"`
	EventsMax    int64   `json:"events_max,omitempty"`
	EventsOOM    int64   `json:"events_oom_kill,omitempty"`
	PSISomeRatio float64 `json:"psi_some_ratio,omitempty"`
	State        string  `json:"state,omitempty"`
}

// memorySample is one full sample: pod plus every tracked component. Each
// persisted NDJSON line and current.json share this schema.
type memorySample struct {
	TS         string            `json:"ts"`
	Mode       MemoryMode        `json:"mode"`
	Pod        podMemory         `json:"pod"`
	Components []componentMemory `json:"components"`
}

// memoryMonitor samples per-component and pod memory on a fixed cadence,
// assesses each component against its derived budget, keeps a bounded in-memory
// history for display, and persists a rolling series for post-restart
// debugging. It never enforces; it collects, assesses, and displays.
//
// The monitor is strictly additive: a nil monitor (subsystem disabled or an
// unsupported platform) makes every method a no-op so the supervisor behaves
// exactly as it does without it.
type memoryMonitor struct {
	cfg          *MemoryConfig
	mode         MemoryMode
	limit        globalLimit
	machineTotal int64 // host RAM, context only (0 if unknown)
	cgroupLimit  int64 // cgroup memory.max, context only (0 if none/unknown)
	budgets      map[string]componentBudget
	tracked      map[string]bool
	comps        []*Component
	bundle       *statekitBundle
	logger       *log.Logger
	persist      *memoryPersister

	// cgroup is the cgroup v2 leaf manager (nil off cgroup2 or when setup
	// failed). When non-nil, per-component CurrentBytes comes from the exact
	// leaf memory.current and the enforcer acts on the leaves.
	cgroup cgroupManager
	// enforce drives soft/hard/pod-pressure actions. nil when not enforcing
	// (no cgroup manager, or the global limit is unresolved so there are no
	// byte budgets to enforce).
	enforce *memoryEnforcer
	// pss tracks the optional smaps_rollup cadence; pssEvery is 0 when disabled.
	pssEvery time.Duration
	pssNext  time.Time

	// now is the clock, overridable in tests.
	now func() time.Time

	incidentSamples int

	mu         sync.RWMutex
	latest     memorySample
	ring       []memorySample
	ringMax    int
	lastEvents map[string]memoryEvent
	degraded   string // non-empty when the subsystem is running degraded (fail-open)
}

// newMemoryMonitor builds the monitor from resolved config, or returns nil when
// the subsystem is disabled or the platform offers nothing to sample. comps are
// the live managed components, queried each tick for their current PIDs.
func newMemoryMonitor(cfg Config, paths Paths, bundle *statekitBundle, comps []*Component, logger *log.Logger) *memoryMonitor {
	mode := resolveMemoryMode(cfg.Memory)
	if mode == MemoryModeDisabled {
		return nil
	}
	limit := resolveGlobalLimit(cfg.Memory)
	budgets := deriveBudgets(limit.Bytes, cfg.Memory, cfg.Components)

	// Static context figures, resolved once: the host's total RAM and the
	// cgroup's own memory.max (shown even when the env var wins the limit).
	machineTotal, _ := readMachineTotalBytes()
	cgroupLimit, _, _ := readCgroupGlobalLimit()

	tracked := make(map[string]bool, len(cfg.Components))
	for _, c := range cfg.Components {
		tracked[c.Name] = c.Memory.IsTracked()
	}

	ringMax := 1
	if cfg.Memory.SampleInterval > 0 {
		ringMax = int(cfg.Memory.RawWindow / cfg.Memory.SampleInterval)
	}
	ringMax = clampInt(ringMax, 1, 4096)

	// Build the cgroup v2 leaf hierarchy when the platform supports it. This
	// creates supervisor/, workload/, and one leaf per component, and moves the
	// supervisor (PID 1) into supervisor/. nil on any non-cgroup2 platform or on
	// a setup failure, in which case sampling falls back to RSS and nothing is
	// enforced. Built from every component name (not just tracked) so each gets
	// a leaf for accounting.
	compNames := make([]string, 0, len(cfg.Components))
	for _, c := range cfg.Components {
		compNames = append(compNames, c.Name)
	}
	// Fail-open: a panic anywhere in cgroup setup must never abort supervisor
	// startup. Recover to a nil manager (tracking-only) instead of propagating.
	var cgroup cgroupManager
	func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Warn("memory: cgroup setup panicked (%v); enforcement disabled, tracking only", r)
				cgroup = nil
			}
		}()
		cgroup = newCgroupManager(mode, compNames, logger)
	}()

	pssEvery := cfg.Memory.PSSInterval
	if mode != MemoryModeCgroup2 && mode != MemoryModeCgroup1 && mode != MemoryModeHost {
		pssEvery = 0
	}

	this := &memoryMonitor{
		cfg:             cfg.Memory,
		mode:            mode,
		limit:           limit,
		machineTotal:    machineTotal,
		cgroupLimit:     cgroupLimit,
		budgets:         budgets,
		tracked:         tracked,
		comps:           comps,
		bundle:          bundle,
		logger:          logger,
		persist:         newMemoryPersister(memoryDir(paths), cfg.Memory.RawWindow, cfg.Memory.Retention, logger),
		now:             time.Now,
		ringMax:         ringMax,
		incidentSamples: cfg.Memory.IncidentSamples,
		cgroup:          cgroup,
		pssEvery:        pssEvery,
	}

	// Axis A — kernel enforcement (cgroup2 only): write the derived per-leaf and
	// workload limits so the kernel throttles at memory.high and OOM-kills the
	// leaf at memory.max. Capability-bound; nil off cgroup2.
	if cgroup != nil {
		pools := derivePools(limit.Bytes, cfg.Memory)
		if pools.SoftPool > 0 {
			cgroup.writeWorkloadHigh(pools.SoftPool)
		}
		for _, c := range cfg.Components {
			b := budgets[c.Name]
			// oom.group is always on: a component that forks workers is killed as
			// a clean unit rather than left half-alive.
			cgroup.writeComponentLimits(c.Name, b.HighBytes, b.LimitBytes, true)
		}
	}

	// Axis B — supervisor "choose" actions: react to each component's memory
	// state (warn/fail) with its pressure_action. A SEPARATE axis from the
	// kernel primitives — platform-independent (acts on the leaf figure under
	// cgroup2, RSS otherwise), so it also runs in host mode on the dev box. It
	// needs the actions enabled and at least one component with a real budget:
	// either a share (which needs a resolved global limit) or an absolute
	// hardlimit (which does not). Without any budget there are no warn/fail
	// states to react to, so the enforcer would have nothing to do.
	if cfg.Memory.IsEnforcing() && anyBudgeted(budgets) {
		this.enforce = newMemoryEnforcer(cfg.Components, cfg.Memory, comps, this, logger)
	}

	if bundle != nil {
		bundle.observeMemoryMode(mode)
		bundle.observeMemoryContext(machineTotal, cgroupLimit)
		if limit.Resolved() {
			bundle.observeGlobalLimit(limit.Bytes)
		}
	}
	logger.Status("memory: mode=%s global_limit=%s (%s) cgroup_limit=%s machine_total=%s sample_interval=%s",
		mode, humanBytes(limit.Bytes), limit.Source, humanBytes(cgroupLimit), humanBytes(machineTotal), cfg.Memory.SampleInterval)

	// Seed the memory-subsystem health state with what the two axes are actually
	// doing: kernel enforcement (cgroup2 leaves) and/or supervisor reactions.
	if bundle != nil {
		kernel := cgroup != nil
		super := this.enforce != nil
		var detail string
		switch {
		case kernel && super:
			detail = "cgroup2: kernel enforcement + supervisor reactions"
		case kernel:
			detail = "cgroup2: kernel enforcement (supervisor reactions off)"
		case super:
			detail = string(mode) + ": supervisor reactions (no kernel backstop)"
		default:
			detail = string(mode) + ": tracking only"
		}
		bundle.observeMemorySubsystemHealthy(detail)
	}

	// Fail-open visibility: cgroup2 was detected (enforcement was expected) but
	// the leaf hierarchy could not be built. The supervisor runs exactly as it
	// does without enforcement — the only effect is the memory.subsystem state
	// shows warn, so an operator sees the promised enforcement is not active.
	if mode == MemoryModeCgroup2 && cgroup == nil {
		// The kernel backstop (memory.high/max on leaves) is gone, but Axis B —
		// the supervisor's own RSS-driven reactions — still runs if it was
		// enabled and a budget exists, so this is not "no enforcement".
		reason := "cgroup2 detected but leaf hierarchy unavailable; "
		if this.enforce != nil {
			reason += "no kernel backstop, supervisor reactions only"
		} else {
			reason += "tracking only, no enforcement"
		}
		this.markDegraded(reason)
	}
	return this
}

// markDegraded records that the memory subsystem could not do what its resolved
// mode promised (leaf setup failed, or a sample/enforcement pass panicked). It
// is purely a visibility signal: the supervisor keeps running unchanged and each
// component's memory health leaf is raised to warn on the next sample. Fail-open
// by construction — nothing here stops supervision. Idempotent per reason.
func (this *memoryMonitor) markDegraded(reason string) {
	if this == nil {
		return
	}
	this.mu.Lock()
	changed := this.degraded != reason
	this.degraded = reason
	this.mu.Unlock()
	if !changed {
		return
	}
	this.logger.Warn("memory: DEGRADED — %s (supervisor continues; memory.subsystem state set to warn)", reason)
	if this.bundle != nil {
		this.bundle.observeMemorySubsystemDegraded(reason)
	}
}

// degradedReason returns the current degraded reason (empty when healthy).
func (this *memoryMonitor) degradedReason() string {
	if this == nil {
		return ""
	}
	this.mu.RLock()
	defer this.mu.RUnlock()
	return this.degraded
}

// Run drives the sampling loop until ctx is cancelled. Modelled on
// runHealthProbe: immediate first sample, then a steady ticker.
func (this *memoryMonitor) Run(ctx context.Context) {
	if this == nil {
		return
	}
	this.persist.pruneExpired(this.now())
	this.reconstructPodOOM()
	this.sampleOnce()

	t := time.NewTicker(this.cfg.SampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			this.sampleOnce()
		}
	}
}

// sampleOnce reads every tracked component's RSS, assembles a sample, assesses
// each against its budget, updates metrics, stores it for display, and
// persists it. Any failure to read one component drops that component from the
// sample rather than failing the tick.
func (this *memoryMonitor) sampleOnce() {
	if this == nil {
		return
	}
	// Fail-open: a panic in any reader (a malformed cgroup file, a racing PID)
	// must not kill the sampling loop or take down the supervisor. Recover,
	// degrade to warn, and let the next tick try again.
	defer func() {
		if r := recover(); r != nil {
			this.markDegraded(fmt.Sprintf("memory sample panicked: %v", r))
		}
	}()
	ts := this.now().UTC()
	pssDue := this.pssEvery > 0 && !ts.Before(this.pssNext)

	var summedRSS int64
	comps := make([]componentMemory, 0, len(this.comps))
	events := make(map[string]memEvents, len(this.comps))
	for _, c := range this.comps {
		name := c.Name()
		if !this.tracked[name] {
			continue
		}
		pid := c.PID()
		budget := this.budgets[name]
		cm := componentMemory{
			Name:       name,
			PID:        pid,
			Share:      budget.Share,
			HighBytes:  budget.HighBytes,
			LimitBytes: budget.LimitBytes,
		}

		// RSS stays a cheap per-process signal in every mode.
		if rss, ok := readProcessRSS(pid); ok {
			cm.RSSBytes = rss
			cm.CurrentBytes = rss // overwritten by the exact leaf figure below.
			summedRSS += rss
		}

		// Under cgroup2 the leaf is authoritative: memory.current is the exact,
		// non-overlapping charge, and stat/events/PSI add the detail RSS lacks.
		if this.cgroup != nil {
			if cur, ok := this.cgroup.readCurrent(name); ok {
				cm.CurrentBytes = cur
			}
			if st, ok := this.cgroup.readStat(name); ok {
				cm.AnonBytes, cm.FileBytes, cm.SlabBytes, cm.SockBytes = st.Anon, st.File, st.Slab, st.Sock
			}
			if ev, ok := this.cgroup.readEvents(name); ok {
				cm.EventsHigh, cm.EventsMax, cm.EventsOOM = ev.High, ev.Max, ev.OOMKill
				events[name] = ev
			}
			if psi, ok := this.cgroup.readPSISome(name); ok {
				cm.PSISomeRatio = psi
			}
		}

		// Optional PSS for deeper diagnosis, read on the slower cadence only.
		if pssDue && pid > 0 {
			if pss, ok := readProcessPSS(pid); ok {
				cm.PSSBytes = pss
			}
		}

		cm.State = classifyMemoryState(cm.CurrentBytes, cm.HighBytes, cm.LimitBytes)
		comps = append(comps, cm)

		if this.bundle != nil {
			this.bundle.observeMemory(name, cm.CurrentBytes, cm.HighBytes, cm.LimitBytes, cm.State)
			this.bundle.observeMemoryDetail(name, cm.RSSBytes)
		}
	}
	sort.Slice(comps, func(i, j int) bool { return comps[i].Name < comps[j].Name })
	if pssDue {
		this.pssNext = ts.Add(this.pssEvery)
	}

	pod := podMemory{
		LimitBytes:        this.limit.Bytes,
		LimitSource:       this.limit.Source,
		MachineTotalBytes: this.machineTotal,
		CgroupLimitBytes:  this.cgroupLimit,
	}
	// Workload total: the exact workload/ cgroup charge when available, else the
	// summed RSS approximation.
	if this.cgroup != nil {
		if wc, ok := this.cgroup.readWorkloadCurrent(); ok {
			pod.WorkloadCurrentBytes = wc
		}
		if psi, ok := this.cgroup.readContainerPSISome(); ok {
			pod.PSISomeRatio = psi
		}
	}
	if pod.WorkloadCurrentBytes == 0 {
		pod.WorkloadCurrentBytes = summedRSS
	}
	// Container charge is the source of truth for the pod-level decision.
	if container, ok := readContainerCurrentBytes(); ok {
		pod.CurrentBytes = container
	} else {
		pod.CurrentBytes = pod.WorkloadCurrentBytes
	}

	sample := memorySample{
		TS:         ts.Format(time.RFC3339),
		Mode:       this.mode,
		Pod:        pod,
		Components: comps,
	}

	if this.bundle != nil {
		this.bundle.observePodMemory(pod.LimitBytes, pod.CurrentBytes, pod.WorkloadCurrentBytes)
		this.bundle.observeMemorySampleTime(ts)
		if selfRSS, ok := readProcessRSS(os.Getpid()); ok {
			this.bundle.observeSupervisorRSS(selfRSS)
		}
	}

	this.store(sample)
	this.persist.write(sample, ts)

	// Enforcement acts last, on the assembled sample plus the fresh event
	// counters. nil off cgroup2 or when the global limit is unresolved. Its own
	// recover means a bug in an enforcement decision degrades to warn and never
	// disturbs tracking/persistence (which already ran above) or the loop.
	func() {
		defer func() {
			if r := recover(); r != nil {
				this.markDegraded(fmt.Sprintf("memory enforcement panicked: %v", r))
			}
		}()
		this.enforce.onSample(sample, events, ts)
	}()
}

// store records the sample as the latest and appends it to the bounded ring.
func (this *memoryMonitor) store(sample memorySample) {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.latest = sample
	this.ring = append(this.ring, sample)
	if len(this.ring) > this.ringMax {
		this.ring = this.ring[len(this.ring)-this.ringMax:]
	}
}

// latestSample returns a copy of the most recent sample for display surfaces.
func (this *memoryMonitor) latestSample() memorySample {
	if this == nil {
		return memorySample{}
	}
	this.mu.RLock()
	defer this.mu.RUnlock()
	return this.latest
}

// componentLatest returns the latest figures for one component, ok=false if it
// has not been sampled yet.
func (this *memoryMonitor) componentLatest(name string) (componentMemory, bool) {
	if this == nil {
		return componentMemory{}, false
	}
	this.mu.RLock()
	defer this.mu.RUnlock()
	for _, c := range this.latest.Components {
		if c.Name == name {
			return c, true
		}
	}
	return componentMemory{}, false
}

// seriesPoint is one (timestamp, current) pair on a component's history.
type seriesPoint struct {
	TS           string `json:"ts"`
	CurrentBytes int64  `json:"current_bytes"`
	HighBytes    int64  `json:"high_bytes,omitempty"`
	LimitBytes   int64  `json:"limit_bytes,omitempty"`
	State        string `json:"state,omitempty"`
}

// componentSeries returns a component's history within the window (counted back
// from the latest sample), merging the two persistence tiers: the in-memory
// ring supplies fine-grained recent raw points (within raw_window), and the
// rollup tier on disk supplies 1-minute points for everything older, so a
// long-range view stays cheap and gap-free. A zero window returns everything
// retained.
func (this *memoryMonitor) componentSeries(name string, window time.Duration) []seriesPoint {
	if this == nil {
		return nil
	}

	this.mu.RLock()
	var lastTS time.Time
	if n := len(this.ring); n > 0 {
		lastTS, _ = time.Parse(time.RFC3339, this.ring[n-1].TS)
	}
	var cutoff time.Time
	if window > 0 && !lastTS.IsZero() {
		cutoff = lastTS.Add(-window)
	}
	ringPts := make([]seriesPoint, 0, len(this.ring))
	var oldestRing time.Time
	for _, s := range this.ring {
		t, err := time.Parse(time.RFC3339, s.TS)
		if err == nil && !cutoff.IsZero() && t.Before(cutoff) {
			continue
		}
		for _, c := range s.Components {
			if c.Name != name {
				continue
			}
			if err == nil && (oldestRing.IsZero() || t.Before(oldestRing)) {
				oldestRing = t
			}
			ringPts = append(ringPts, seriesPoint{
				TS:           s.TS,
				CurrentBytes: c.CurrentBytes,
				HighBytes:    c.HighBytes,
				LimitBytes:   c.LimitBytes,
				State:        c.State,
			})
		}
	}
	this.mu.RUnlock()

	// Fill the older portion from the rollup tier (minute granularity), keeping
	// only points the ring doesn't already cover. Read outside the lock.
	var older []seriesPoint
	for _, p := range this.persist.readRollupSeries(name, cutoff) {
		t, err := time.Parse(time.RFC3339, p.TS)
		if err != nil {
			continue
		}
		if !oldestRing.IsZero() && !t.Before(oldestRing) {
			continue // the ring already covers this minute at finer granularity
		}
		older = append(older, p)
	}
	return append(older, ringPts...)
}

// enrichSnapshot fills the memory fields on a component snapshot from the latest
// sample. A no-op for a nil monitor or an unsampled component, so non-memory
// builds and tracking-only modes render cleanly.
func (this *memoryMonitor) enrichSnapshot(snap *ComponentSnapshot) {
	if this == nil || snap == nil {
		return
	}
	if e, ok := this.componentLastEvent(snap.Name); ok {
		snap.MemoryLastEvent = e.String()
	}
	cm, ok := this.componentLatest(snap.Name)
	if !ok {
		return
	}
	snap.MemoryCurrentBytes = cm.CurrentBytes
	snap.MemoryHighBytes = cm.HighBytes
	snap.MemoryLimitBytes = cm.LimitBytes
	snap.MemoryState = cm.State
	if cm.LimitBytes > 0 {
		snap.MemoryPressureRatio = float64(cm.CurrentBytes) / float64(cm.LimitBytes)
	}
}

// reconstructPodOOM runs once at startup, before the first sample overwrites
// current.json. When the previous run's last persisted sample showed the pod
// near its limit (a whole-cgroup OOM leaves no exit handler to run, so the
// rolling files are the only evidence), it writes a pod_oom_reconstructed
// incident from those files so post-restart diagnosis is possible. Best-effort
// and heuristic: a clean restart at low usage produces nothing.
func (this *memoryMonitor) reconstructPodOOM() {
	if this == nil || this.cfg == nil {
		return
	}
	last, ok := this.persist.loadLastSample()
	if !ok || last.Pod.LimitBytes <= 0 || last.Pod.CurrentBytes <= 0 {
		return
	}
	ratio := float64(last.Pod.CurrentBytes) / float64(last.Pod.LimitBytes)
	if ratio < this.cfg.PodPressureHigh {
		return // last run ended well below the limit — not an OOM signature.
	}

	ts := this.now().UTC()
	samples := this.persist.loadRecentRawSamples(this.incidentSamples)
	incident := memoryIncident{
		TS:      ts.Format(time.RFC3339),
		Kind:    memIncidentPodOOMRecon,
		Reason:  "previous run ended at " + percentString(ratio) + " of the pod limit; likely whole-pod OOM",
		Mode:    this.mode,
		Samples: samples,
	}
	this.persist.writeIncident(incident, ts)
	this.logger.Warn("memory: reconstructed likely pod OOM from prior run (last usage %s of limit)", percentString(ratio))
}

// leafAttacher returns the launch hook that charges a component's child into
// its cgroup leaf, or nil when there are no leaves (so the component's launch
// stays untouched). Wired onto each Component at supervisor construction.
func (this *memoryMonitor) leafAttacher(name string) func(cmd *exec.Cmd) func(pid int) {
	if this == nil || this.cgroup == nil {
		return nil
	}
	return this.cgroup.leafAttach(name)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
