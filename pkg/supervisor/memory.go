package supervisor

import (
	"context"
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
	LimitBytes           int64  `json:"limit_bytes,omitempty"`
	LimitSource          string `json:"limit_source,omitempty"`
	MachineTotalBytes    int64  `json:"machine_total_bytes,omitempty"`
	CgroupLimitBytes     int64  `json:"cgroup_limit_bytes,omitempty"`
	CurrentBytes         int64  `json:"current_bytes,omitempty"`
	WorkloadCurrentBytes int64  `json:"workload_current_bytes,omitempty"`
}

// componentMemory is one component's figures in a sample. In this tracking-only
// iteration CurrentBytes equals RSSBytes (process RSS); HighBytes/LimitBytes are
// the derived budgets used purely for assessment and display.
type componentMemory struct {
	Name         string  `json:"name"`
	PID          int     `json:"pid,omitempty"`
	Share        float64 `json:"share,omitempty"`
	CurrentBytes int64   `json:"current_bytes"`
	RSSBytes     int64   `json:"rss_bytes,omitempty"`
	HighBytes    int64   `json:"high_bytes,omitempty"`
	LimitBytes   int64   `json:"limit_bytes,omitempty"`
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

	// now is the clock, overridable in tests.
	now func() time.Time

	incidentSamples int

	mu         sync.RWMutex
	latest     memorySample
	ring       []memorySample
	ringMax    int
	lastEvents map[string]memoryEvent
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
		persist:         newMemoryPersister(memoryDir(paths), cfg.Memory.Retention, logger),
		now:             time.Now,
		ringMax:         ringMax,
		incidentSamples: cfg.Memory.IncidentSamples,
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
	return this
}

// Run drives the sampling loop until ctx is cancelled. Modelled on
// runHealthProbe: immediate first sample, then a steady ticker.
func (this *memoryMonitor) Run(ctx context.Context) {
	if this == nil {
		return
	}
	this.persist.pruneExpired(this.now())
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
	ts := this.now().UTC()

	var workload int64
	comps := make([]componentMemory, 0, len(this.comps))
	for _, c := range this.comps {
		name := c.Name()
		if !this.tracked[name] {
			continue
		}
		pid := c.PID()
		rss, ok := readProcessRSS(pid)
		budget := this.budgets[name]
		cm := componentMemory{
			Name:       name,
			PID:        pid,
			Share:      budget.Share,
			HighBytes:  budget.HighBytes,
			LimitBytes: budget.LimitBytes,
		}
		if ok {
			cm.CurrentBytes = rss
			cm.RSSBytes = rss
			workload += rss
		}
		cm.State = classifyMemoryState(cm.CurrentBytes, cm.HighBytes, cm.LimitBytes)
		comps = append(comps, cm)

		if this.bundle != nil {
			this.bundle.observeMemory(name, cm.CurrentBytes, cm.HighBytes, cm.LimitBytes, cm.State)
		}
	}
	sort.Slice(comps, func(i, j int) bool { return comps[i].Name < comps[j].Name })

	pod := podMemory{
		LimitBytes:           this.limit.Bytes,
		LimitSource:          this.limit.Source,
		MachineTotalBytes:    this.machineTotal,
		CgroupLimitBytes:     this.cgroupLimit,
		WorkloadCurrentBytes: workload,
	}
	if container, ok := readContainerCurrentBytes(); ok {
		pod.CurrentBytes = container
	} else {
		pod.CurrentBytes = workload
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
	}

	this.store(sample)
	this.persist.write(sample, ts)
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

// componentSeries returns the in-memory history for one component within the
// window (counted back from the latest sample). A zero window returns all
// retained points.
func (this *memoryMonitor) componentSeries(name string, window time.Duration) []seriesPoint {
	if this == nil {
		return nil
	}
	this.mu.RLock()
	defer this.mu.RUnlock()

	var cutoff time.Time
	if window > 0 && len(this.ring) > 0 {
		if last, err := time.Parse(time.RFC3339, this.ring[len(this.ring)-1].TS); err == nil {
			cutoff = last.Add(-window)
		}
	}
	out := make([]seriesPoint, 0, len(this.ring))
	for _, s := range this.ring {
		if !cutoff.IsZero() {
			if t, err := time.Parse(time.RFC3339, s.TS); err == nil && t.Before(cutoff) {
				continue
			}
		}
		for _, c := range s.Components {
			if c.Name != name {
				continue
			}
			out = append(out, seriesPoint{
				TS:           s.TS,
				CurrentBytes: c.CurrentBytes,
				HighBytes:    c.HighBytes,
				LimitBytes:   c.LimitBytes,
				State:        c.State,
			})
		}
	}
	return out
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

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
