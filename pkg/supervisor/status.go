package supervisor

// ComponentSnapshot is the JSON-friendly summary of one component, populated
// by Component.Snapshot() and embedded in /info and /summary responses.
//
// Status is the statekit status of the lifecycle leaf ("pass" / "warn" /
// "fail" / "down") — there is no separate supervisor vocabulary. The
// human-readable reason text lives on the leaf and is surfaced via /state.
//
// Port is the configured port the child binds. MonitorURLs gives the
// path layout (healthz/readyz/state/metrics) the supervisor expects the child
// to serve, so the control plane can compose URLs without re-reading the
// supervisor config.
type ComponentSnapshot struct {
	Name          string      `json:"name"`
	Description   string      `json:"description,omitempty"`
	Stable        string      `json:"stable,omitempty"`
	Current       string      `json:"current,omitempty"`
	Status        string      `json:"status"`
	Port          int         `json:"port,omitempty"`
	ChildPID      int         `json:"child_pid,omitempty"`
	UptimeSeconds int64       `json:"uptime_seconds,omitempty"`
	FastCrashes   int         `json:"fast_crashes"`
	ExecFailures  int         `json:"exec_failures"`
	MonitorURLs   MonitorURLs `json:"monitor_urls,omitzero"`
}

// MonitorURLs is the snapshot-time copy of the URLsConfig path layout. Lives
// in status.go so the JSON tags are stable and independent of any config
// reshuffling.
type MonitorURLs struct {
	Healthz string `json:"healthz,omitempty"`
	Readyz  string `json:"readyz,omitempty"`
	State   string `json:"state,omitempty"`
	Metrics string `json:"metrics,omitempty"`
}

// SupervisorSnapshot is the top-level shape of /state.
type SupervisorSnapshot struct {
	StateDir      string              `json:"state_dir"`
	StartedAt     string              `json:"started_at"`
	LastPollAt    string              `json:"last_poll_at,omitempty"`
	LastPollError string              `json:"last_poll_error,omitempty"`
	ForceOverride map[string]string   `json:"force_overrides,omitempty"`
	Components    []ComponentSnapshot `json:"components"`
}
