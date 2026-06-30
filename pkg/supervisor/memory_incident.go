package supervisor

import (
	"strings"
	"time"
)

// Incident kinds. In this tracking-only iteration the only trigger is an
// abnormal child exit; enforcement phases add soft_restart/hard_restart/
// oom_kill, and a pod_oom_reconstructed kind written on the next start after a
// whole-pod OOM (which needs the cgroup oom_kill counter, deferred to Track A).
const memIncidentChildExit = "child_exit"

// memoryIncident wraps the trigger and the preceding samples, so post-restart
// diagnosis is possible even after Kubernetes events have expired. It shares
// the sample schema with the rolling series.
type memoryIncident struct {
	TS        string         `json:"ts"`
	Kind      string         `json:"kind"`
	Component string         `json:"component,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Mode      MemoryMode     `json:"mode"`
	Samples   []memorySample `json:"samples"`
}

// memoryEvent is the last memory-related event recorded for a component,
// surfaced as memory_last_event on the snapshot.
type memoryEvent struct {
	TS   string
	Kind string
}

// String renders the event for display, e.g. "child_exit at 2026-06-30T05:36:55Z".
func (this memoryEvent) String() string {
	if this.TS == "" {
		return ""
	}
	return this.Kind + " at " + this.TS
}

// captureIncident snapshots the last incident_samples samples into an incident
// file and records the component's last memory event. Safe on a nil monitor and
// callable from the lifecycle goroutine on an abnormal child exit.
func (this *memoryMonitor) captureIncident(component, kind, reason string) {
	if this == nil {
		return
	}
	ts := this.now().UTC()

	this.mu.Lock()
	n := this.incidentSamples
	if n <= 0 || n > len(this.ring) {
		n = len(this.ring)
	}
	samples := make([]memorySample, n)
	copy(samples, this.ring[len(this.ring)-n:])
	if this.lastEvents == nil {
		this.lastEvents = make(map[string]memoryEvent)
	}
	this.lastEvents[component] = memoryEvent{TS: ts.Format(time.RFC3339), Kind: kind}
	this.mu.Unlock()

	incident := memoryIncident{
		TS:        ts.Format(time.RFC3339),
		Kind:      kind,
		Component: component,
		Reason:    reason,
		Mode:      this.mode,
		Samples:   samples,
	}
	this.persist.writeIncident(incident, ts)
}

// componentLastEvent returns the last recorded memory event for a component.
func (this *memoryMonitor) componentLastEvent(name string) (memoryEvent, bool) {
	if this == nil {
		return memoryEvent{}, false
	}
	this.mu.RLock()
	defer this.mu.RUnlock()
	e, ok := this.lastEvents[name]
	return e, ok
}

// listIncidents returns incident metadata (newest first) for the listing
// endpoint, without the heavy sample payload.
func (this *memoryMonitor) listIncidents() []incidentMeta {
	if this == nil {
		return nil
	}
	return this.persist.listIncidents()
}

// incidentTimestampLayout is a filesystem-safe RFC3339 (colons replaced) used in
// incident filenames, e.g. 2026-06-30T05-36-55Z-child_exit.json.
func incidentFilename(ts time.Time, kind string) string {
	stamp := strings.ReplaceAll(ts.UTC().Format(time.RFC3339), ":", "-")
	return stamp + "-" + kind + ".json"
}
