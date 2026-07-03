package supervisor

import (
	"sync"
	"time"

	"github.com/gur-shatz/go-run/internal/log"
)

// memoryEnforcer watches each component's memory STATE (pass/warn/fail) and,
// once a non-monitor_only component has been fail (over its hard threshold) for
// the global sustained_for, terminates the child. The termination surfaces
// through the normal exit path, so a memory kill is handled exactly like the
// process crashing on its own — fast-crash accounting, backoff, and bad-version
// rollback all apply unchanged. warn is a visible early signal only.
//
// Under cgroup2 the kernel (memory.max) may kill first; either way the child
// exit is an ordinary crash. foldEvents additionally records a memory oom_kill
// incident + metric for attribution when the leaf's counter rises.
//
// It runs entirely on the memory sampling goroutine via onSample. The enforcer
// is created only when the global memory.enforce switch is on and at least one
// component has a real budget; nil otherwise, and every method no-ops on nil.
type memoryEnforcer struct {
	cfg     *MemoryConfig
	monitor *memoryMonitor
	logger  *log.Logger

	monitorOnly map[string]bool       // components exempt from killing
	comp        map[string]*Component // live handles
	order       []string

	mu             sync.Mutex
	failSince      map[string]time.Time // first tick the state went fail (zero = not fail)
	killedPID      map[string]int       // PID already terminated (don't re-kill the same child)
	lastEvents     map[string]memEvents // previous leaf counters, to detect increments
	seenEvents     map[string]bool      // whether lastEvents has a baseline yet
	podSettleUntil time.Time
}

// podPressureSettle is the recovery pause after a pod-pressure kill before the
// enforcer will shed another component.
const podPressureSettle = 30 * time.Second

// newMemoryEnforcer wires the per-component monitor_only flags and live handles.
func newMemoryEnforcer(cfgs []ComponentConfig, mem *MemoryConfig, comps []*Component, monitor *memoryMonitor, logger *log.Logger) *memoryEnforcer {
	byName := make(map[string]*Component, len(comps))
	for _, c := range comps {
		byName[c.Name()] = c
	}
	this := &memoryEnforcer{
		cfg:         mem,
		monitor:     monitor,
		logger:      logger,
		monitorOnly: make(map[string]bool, len(cfgs)),
		comp:        make(map[string]*Component, len(cfgs)),
		order:       make([]string, 0, len(cfgs)),
		failSince:   make(map[string]time.Time),
		killedPID:   make(map[string]int),
		lastEvents:  make(map[string]memEvents),
		seenEvents:  make(map[string]bool),
	}
	for _, c := range cfgs {
		if c.Memory == nil {
			continue
		}
		this.monitorOnly[c.Name] = c.Memory.IsMonitorOnly()
		if live := byName[c.Name]; live != nil {
			this.comp[c.Name] = live
		}
		this.order = append(this.order, c.Name)
	}
	return this
}

// sustainedFor is the global fail-duration before a kill.
func (this *memoryEnforcer) sustainedFor() time.Duration {
	if this.cfg != nil && this.cfg.SustainedFor > 0 {
		return this.cfg.SustainedFor
	}
	return 60 * time.Second
}

// onSample is the per-tick enforcement pass: fold leaf event counters for
// attribution, advance each component's fail timer and terminate on a sustained
// fail, then evaluate pod pressure.
func (this *memoryEnforcer) onSample(sample memorySample, events map[string]memEvents, now time.Time) {
	if this == nil {
		return
	}
	this.mu.Lock()
	defer this.mu.Unlock()

	byName := make(map[string]componentMemory, len(sample.Components))
	for _, cm := range sample.Components {
		byName[cm.Name] = cm
	}

	for _, name := range this.order {
		cm := byName[name]

		// Attribution only: a rising leaf oom_kill/high counter is recorded, but
		// the kernel's kill still surfaces as an ordinary child exit (crash).
		if ev, ok := events[name]; ok {
			this.foldEvents(name, ev)
		}

		if this.monitorOnly[name] {
			this.failSince[name] = time.Time{}
			continue // tracked + warn/fail state, but never killed
		}

		// The one rule: once the memory state has been fail continuously for
		// sustained_for, terminate the child. warn is a visible signal only.
		if cm.State == memStateHard {
			if this.failSince[name].IsZero() {
				this.failSince[name] = now
			}
			if now.Sub(this.failSince[name]) >= this.sustainedFor() && this.killedPID[name] != cm.PID {
				this.terminate(name, cm.PID, memIncidentHardRestart,
					"memory state fail for "+this.sustainedFor().String())
			}
		} else {
			this.failSince[name] = time.Time{}
		}
	}

	this.evaluatePodPressure(sample, byName, now)
}

// terminate kills a component's child because it breached its budget. Caller
// holds mu. The kill is recorded for attribution and then delegated to
// Component.TerminateForMemory, whose exit flows through the normal crash path.
// killedPID guards against re-killing the same child while it drains.
func (this *memoryEnforcer) terminate(name string, pid int, kind, reason string) {
	live := this.comp[name]
	if live == nil || pid <= 0 {
		return
	}
	this.killedPID[name] = pid
	this.failSince[name] = time.Time{}
	if this.monitor != nil {
		this.monitor.captureIncident(name, kind, reason)
		if this.monitor.bundle != nil {
			this.monitor.bundle.observeMemoryRestart(name, kind)
		}
	}
	this.logger.Status("memory: terminating %s (pid=%d) — %s; handled as a crash", name, pid, reason)
	go live.TerminateForMemory(reason)
}

// evaluatePodPressure is the aggregate safety net: when the whole pod is near
// its limit even though no single component has breached its own budget, it
// terminates the largest killable component. Caller holds mu.
func (this *memoryEnforcer) evaluatePodPressure(sample memorySample, byName map[string]componentMemory, now time.Time) {
	L := sample.Pod.LimitBytes
	if L <= 0 {
		return // no resolved limit; nothing to measure pressure against.
	}
	ratio := float64(sample.Pod.pressureBytes()) / float64(L)
	overUsage := ratio > this.cfg.PodPressureHigh
	overPSI := sample.Pod.PSISomeRatio > 0 && sample.Pod.PSISomeRatio > this.cfg.PodPressurePSI
	if !overUsage && !overPSI {
		return
	}
	if now.Before(this.podSettleUntil) {
		return // still settling from the previous pod-pressure kill.
	}

	name, pid, ok := this.largestKillable(byName)
	if !ok {
		this.logger.Warn("memory: pod pressure (usage=%.0f%% psi=%.2f) but no killable component; deferring to the kernel backstop",
			ratio*100, sample.Pod.PSISomeRatio)
		return
	}
	this.podSettleUntil = now.Add(podPressureSettle)
	this.terminate(name, pid, memIncidentPodPressure, "pod pressure (usage "+percentString(ratio)+")")
}

// killTarget is one component eligible for pod-pressure shedding.
type killTarget struct {
	name string
	pid  int
	cur  int64
}

// largestKillable returns the running, non-monitor_only component using the most
// memory — the one to shed first under pod pressure.
func (this *memoryEnforcer) largestKillable(byName map[string]componentMemory) (string, int, bool) {
	targets := make([]killTarget, 0, len(this.order))
	for _, name := range this.order {
		if this.monitorOnly[name] || this.comp[name] == nil {
			continue
		}
		cm := byName[name]
		if cm.PID == 0 || this.killedPID[name] == cm.PID {
			continue // no live child, or already killed this one — wait for the replacement
		}
		targets = append(targets, killTarget{name: name, pid: cm.PID, cur: cm.pressureBytes()})
	}
	t, ok := pickLargest(targets)
	return t.name, t.pid, ok
}

// pickLargest returns the target with the greatest current usage, or ok=false
// when the set is empty. Ties resolve to the first seen (stable input order).
func pickLargest(targets []killTarget) (killTarget, bool) {
	best, found := killTarget{}, false
	for _, t := range targets {
		if !found || t.cur > best.cur {
			best, found = t, true
		}
	}
	return best, found
}

// foldEvents compares the latest leaf counters against the baseline and records
// attribution (a metric, and an incident on an oom_kill) without changing how
// the exit is handled — the kernel's kill still lands as an ordinary crash.
func (this *memoryEnforcer) foldEvents(name string, ev memEvents) {
	prev, had := this.lastEvents[name], this.seenEvents[name]
	this.lastEvents[name] = ev
	this.seenEvents[name] = true
	if !had || this.monitor == nil {
		return // first observation: establish the baseline without firing.
	}
	if ev.High > prev.High && this.monitor.bundle != nil {
		this.monitor.bundle.observeMemoryEvent(name, "high")
	}
	if ev.OOMKill > prev.OOMKill {
		this.monitor.captureIncident(name, memIncidentOOMKill, "leaf reached memory.max (oom_kill)")
		if this.monitor.bundle != nil {
			this.monitor.bundle.observeMemoryEvent(name, "oom_kill")
		}
		this.logger.Warn("memory: %s OOM-killed in its leaf by the kernel; handled as a crash on exit", name)
	}
}
