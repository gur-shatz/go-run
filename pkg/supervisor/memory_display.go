package supervisor

import (
	"encoding/json"
	"net/http"
	"time"
)

// memorySummary is the /backoffice/memory YAML shape: pod totals plus a
// per-component current/high/limit/share/state list. Byte counts carry a
// human-readable twin so the page is readable without conversion.
type memorySummary struct {
	Mode MemoryMode `yaml:"mode"`
	// Degraded is set when the subsystem is running fail-open (e.g. cgroup2 was
	// detected but leaves could not be built, so enforcement is not active).
	Degraded   string                   `yaml:"degraded,omitempty"`
	Pod        memoryPodSummary         `yaml:"pod"`
	Components []memoryComponentSummary `yaml:"components"`
}

type memoryPodSummary struct {
	LimitBytes              int64   `yaml:"limit_bytes,omitempty"`
	Limit                   string  `yaml:"limit,omitempty"`
	LimitSource             string  `yaml:"limit_source,omitempty"`
	MachineTotalBytes       int64   `yaml:"machine_total_bytes,omitempty"`
	MachineTotal            string  `yaml:"machine_total,omitempty"`
	CgroupLimitBytes        int64   `yaml:"cgroup_limit_bytes,omitempty"`
	CgroupLimit             string  `yaml:"cgroup_limit,omitempty"`
	CurrentBytes            int64   `yaml:"current_bytes,omitempty"`
	Current                 string  `yaml:"current,omitempty"`
	WorkingSetBytes         int64   `yaml:"working_set_bytes,omitempty"`
	WorkingSet              string  `yaml:"working_set,omitempty"`
	InactiveFileBytes       int64   `yaml:"inactive_file_bytes,omitempty"`
	InactiveFile            string  `yaml:"inactive_file,omitempty"`
	WorkloadCurrentBytes    int64   `yaml:"workload_current_bytes,omitempty"`
	WorkloadCurrent         string  `yaml:"workload_current,omitempty"`
	WorkloadWorkingSetBytes int64   `yaml:"workload_working_set_bytes,omitempty"`
	WorkloadWorkingSet      string  `yaml:"workload_working_set,omitempty"`
	PSISomeRatio            float64 `yaml:"psi_some_ratio,omitempty"`
}

type memoryComponentSummary struct {
	Name              string  `yaml:"name"`
	CurrentBytes      int64   `yaml:"current_bytes"`
	Current           string  `yaml:"current"`
	WorkingSetBytes   int64   `yaml:"working_set_bytes,omitempty"`
	WorkingSet        string  `yaml:"working_set,omitempty"`
	InactiveFileBytes int64   `yaml:"inactive_file_bytes,omitempty"`
	InactiveFile      string  `yaml:"inactive_file,omitempty"`
	HighBytes         int64   `yaml:"high_bytes,omitempty"`
	High              string  `yaml:"high,omitempty"`
	LimitBytes        int64   `yaml:"limit_bytes,omitempty"`
	Limit             string  `yaml:"limit,omitempty"`
	Share             float64 `yaml:"share,omitempty"`
	State             string  `yaml:"state,omitempty"`
	// Phase-2 leaf detail, present under cgroup2. The event counters are
	// cumulative for the leaf's lifetime; a rising oom_kill is the smoking gun.
	PSISomeRatio  float64 `yaml:"psi_some_ratio,omitempty"`
	EventsHigh    int64   `yaml:"events_high,omitempty"`
	EventsMax     int64   `yaml:"events_max,omitempty"`
	EventsOOMKill int64   `yaml:"events_oom_kill,omitempty"`
}

// buildMemorySummary renders the one-screen overview from the monitor's latest
// sample. A nil monitor (subsystem off) yields a minimal disabled document.
func buildMemorySummary(mem *memoryMonitor, _ SupervisorSnapshot) memorySummary {
	if mem == nil {
		return memorySummary{Mode: MemoryModeDisabled}
	}
	sample := mem.latestSample()
	out := memorySummary{
		Mode:     sample.Mode,
		Degraded: mem.degradedReason(),
		Pod: memoryPodSummary{
			LimitBytes:              sample.Pod.LimitBytes,
			Limit:                   humanBytes(sample.Pod.LimitBytes),
			LimitSource:             sample.Pod.LimitSource,
			MachineTotalBytes:       sample.Pod.MachineTotalBytes,
			MachineTotal:            humanBytes(sample.Pod.MachineTotalBytes),
			CgroupLimitBytes:        sample.Pod.CgroupLimitBytes,
			CgroupLimit:             humanBytes(sample.Pod.CgroupLimitBytes),
			CurrentBytes:            sample.Pod.CurrentBytes,
			Current:                 humanBytes(sample.Pod.CurrentBytes),
			WorkingSetBytes:         sample.Pod.WorkingSetBytes,
			WorkingSet:              humanBytes(sample.Pod.WorkingSetBytes),
			InactiveFileBytes:       sample.Pod.InactiveFileBytes,
			InactiveFile:            humanBytes(sample.Pod.InactiveFileBytes),
			WorkloadCurrentBytes:    sample.Pod.WorkloadCurrentBytes,
			WorkloadCurrent:         humanBytes(sample.Pod.WorkloadCurrentBytes),
			WorkloadWorkingSetBytes: sample.Pod.WorkloadWorkingSetBytes,
			WorkloadWorkingSet:      humanBytes(sample.Pod.WorkloadWorkingSetBytes),
			PSISomeRatio:            sample.Pod.PSISomeRatio,
		},
		Components: make([]memoryComponentSummary, 0, len(sample.Components)),
	}
	if out.Mode == "" {
		out.Mode = mem.mode
	}
	for _, c := range sample.Components {
		out.Components = append(out.Components, memoryComponentSummary{
			Name:              c.Name,
			CurrentBytes:      c.CurrentBytes,
			Current:           humanBytes(c.CurrentBytes),
			WorkingSetBytes:   c.WorkingSetBytes,
			WorkingSet:        humanBytes(c.WorkingSetBytes),
			InactiveFileBytes: c.InactiveFileBytes,
			InactiveFile:      humanBytes(c.InactiveFileBytes),
			HighBytes:         c.HighBytes,
			High:              humanBytes(c.HighBytes),
			LimitBytes:        c.LimitBytes,
			Limit:             humanBytes(c.LimitBytes),
			Share:             c.Share,
			State:             c.State,
			PSISomeRatio:      c.PSISomeRatio,
			EventsHigh:        c.EventsHigh,
			EventsMax:         c.EventsMax,
			EventsOOMKill:     c.EventsOOM,
		})
	}
	return out
}

// memoryIncidentList is the /backoffice/memory/incidents YAML shape.
type memoryIncidentList struct {
	Incidents []incidentMeta `yaml:"incidents"`
}

// buildIncidentList returns the captured incidents, newest first. A nil monitor
// yields an empty list.
func buildIncidentList(mem *memoryMonitor) memoryIncidentList {
	out := memoryIncidentList{Incidents: mem.listIncidents()}
	if out.Incidents == nil {
		out.Incidents = []incidentMeta{}
	}
	return out
}

// componentMemorySeries is the /backoffice/components/{name}/memory JSON shape.
type componentMemorySeries struct {
	Name   string        `json:"name"`
	Window string        `json:"window"`
	Series []seriesPoint `json:"series"`
}

// writeComponentMemoryJSON serializes one component's bounded history. The
// window defaults to 1h and is clamped to a parseable duration; an unparseable
// value falls back to the default rather than erroring.
func writeComponentMemoryJSON(w http.ResponseWriter, mem *memoryMonitor, name, windowParam string) {
	window := time.Hour
	if windowParam != "" {
		if d, err := time.ParseDuration(windowParam); err == nil && d > 0 {
			window = d
		}
	}
	out := componentMemorySeries{
		Name:   name,
		Window: window.String(),
		Series: mem.componentSeries(name, window),
	}
	if out.Series == nil {
		out.Series = []seriesPoint{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}
