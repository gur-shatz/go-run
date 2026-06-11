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
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	External    bool   `json:"external,omitempty"`
	URL         string `json:"url,omitempty"`
	Stable      string `json:"stable,omitempty"`
	Current     string `json:"current,omitempty"`

	// GlobalState is the component's own top-level health, read from its
	// /healthz: the body ("pass"/"warn"/"fail") when the probe returns 200, or
	// "down" for any non-200 / connection error. Built-in and always on,
	// independent of statemonitor.scrape.
	GlobalState string `json:"global_state,omitempty"`

	// UpdateStatus is the supervisor's versioning posture for this component:
	// "live" (auto-updating), "pinned to stable", or "pinned to <version>".
	UpdateStatus string `json:"update_status,omitempty"`

	// UpdateState/UpdateReason describe the updater leaf: last poll/prepare
	// result, including cases like "target v3 is rejected; holding current".
	UpdateState  string `json:"update_state,omitempty"`
	UpdateReason string `json:"update_reason,omitempty"`

	// Status is the statekit lifecycle status (the supervisor's process view:
	// running / restarting / halted). Distinct from GlobalState, which is the
	// component's self-reported health.
	Status        string `json:"status"`
	StatusReason  string `json:"status_reason,omitempty"`
	Port          int    `json:"port,omitempty"`
	ChildPID      int    `json:"child_pid,omitempty"`
	UptimeSeconds int64  `json:"uptime_seconds,omitempty"`
	RunCount      int64  `json:"run_count"`
	FastCrashes   int    `json:"fast_crashes"`
	ExecFailures  int    `json:"exec_failures"`

	// LastUpgrade is the RFC3339 time the running version last changed, taken
	// from current.txt's mtime (which is only rewritten on an actual version
	// switch, so it survives supervisor restarts). Empty if no version yet.
	LastUpgrade string `json:"last_upgrade,omitempty"`

	MonitorURLs MonitorURLs       `json:"monitor_urls,omitzero"`
	ProxyURLs   map[string]string `json:"proxy_urls,omitempty"`
}

// MonitorURLs is the snapshot-time copy of the URLsConfig path layout. Lives
// in status.go so the JSON tags are stable and independent of any config
// reshuffling.
type MonitorURLs struct {
	Healthz     string `json:"healthz,omitempty"`
	Readyz      string `json:"readyz,omitempty"`
	State       string `json:"state,omitempty"`
	Metrics     string `json:"metrics,omitempty"`
	Escalations string `json:"escalations,omitempty"`
}

// SupervisorSnapshot is the top-level shape of /state.
type SupervisorSnapshot struct {
	StateDir      string              `json:"state_dir"`
	PublicPort    int                 `json:"public_port,omitempty"`
	StartedAt     string              `json:"started_at"`
	LastPollAt    string              `json:"last_poll_at,omitempty"`
	LastPollError string              `json:"last_poll_error,omitempty"`
	ForceOverride map[string]string   `json:"force_overrides,omitempty"`
	Components    []ComponentSnapshot `json:"components"`
}
