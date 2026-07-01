package supervisor

import (
	_ "embed"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gur-shatz/go-run/pkg/config"
)

//go:embed supervisor.default.yaml
var DefaultConfigYAML string

// Config is the top-level supervisor.yml configuration.
type Config struct {
	StateDir string `yaml:"state_dir"`

	// StabilityTime: cumulative uptime at which the supervisor wipes
	// the crash counters (recovery) and, if the version is not rejected,
	// promotes it to stable.
	StabilityTime time.Duration `yaml:"stability_time"`

	// CrashWindow: a child exit with uptime < CrashWindow counts as a
	// fast crash; longer-lived exits restart under backoff without
	// touching the crash counter. Independent of StabilityTime so a
	// flaky-but-not-tight-looping crash (alive 2-4 min) still trips
	// the bad-version criterion well before promotion would.
	CrashWindow            time.Duration `yaml:"crash_window"`
	CrashThreshold         int           `yaml:"crash_threshold"`
	ExecFailThreshold      int           `yaml:"exec_fail_threshold"`
	KillGracePeriod        time.Duration `yaml:"kill_grace_period"`
	VersionFolderRetention int           `yaml:"version_folder_retention"`

	// RejectExpiry: how long an autonomously-recorded rejection stays
	// active. After this duration the supervisor stops treating the
	// version as rejected and is willing to re-install it if the remote
	// still asks for it. If the version is still bad it'll be re-rejected
	// on the next bad-version cycle. Zero disables expiry (rejections
	// stay active forever within this host).
	RejectExpiry time.Duration `yaml:"reject_expiry"`

	// LogMaxSize / LogMaxFiles control rotation of the per-version
	// stdout.log and stderr.log files the supervisor captures. Default
	// 10 MiB per file, 5 history generations. Set LogMaxFiles to 0 to
	// disable history (rotation truncates instead of preserving).
	LogMaxSize  int64 `yaml:"log_max_size"`
	LogMaxFiles int   `yaml:"log_max_files"`

	// Vars is the customer-controlled deployment context. Every key here is
	// exported to each child process and is also available to manifest
	// validate_templates checks (alongside manifest default_vars, launch
	// variables, and env). Precedence on key collisions:
	// launch facts > component env > Vars > manifest default_vars > process env.
	Vars map[string]any `yaml:"vars,omitempty"`

	Supervisor SupervisorConfig `yaml:"supervisor"`
	Updates    UpdatesConfig    `yaml:"updates,omitempty"`
	Remote     RemoteConfig     `yaml:"remote"`

	// Memory configures the per-component memory tracking subsystem. When nil
	// or disabled the supervisor behaves exactly as it does without it: no
	// sampling, no persistence, no memory fields on any surface. This first
	// iteration only collects, assesses, and displays usage — it does not
	// enforce limits.
	Memory *MemoryConfig `yaml:"memory,omitempty"`

	// StateMonitor configures the supervisor's component-monitoring role, in
	// two independently toggleable parts:
	//
	//   - scrape: poll each component's healthz/state/metrics into the registry
	//     (on by default).
	//   - observe: persist that state and serve the /health console (off by
	//     default).
	//
	// Turning scrape off does NOT affect the supervisor's own liveness
	// (/backoffice/healthz), its own metrics, or its per-component
	// process-supervision state (<name>.supervisorstate: running / crashed /
	// uptime). Those come from the component lifecycle, not from scraping.
	// Without scrape you lose only the child's self-reported /state, its
	// /metrics passthrough, and the HTTP liveness probe of the child.
	StateMonitor StateMonitorConfig `yaml:"statemonitor,omitempty"`

	Components []ComponentConfig `yaml:"components"`

	// ExternalComponents are workloads the supervisor does not launch or
	// update, but still exposes in the portal, proxies to, and scrapes.
	ExternalComponents []ExternalComponentConfig `yaml:"external_components,omitempty"`
}

// MemoryConfig is the top-level memory subsystem block. Budgets are never
// written as absolute byte values: the global limit is injected into the
// environment (or read from the cgroup) and supervisor.yml carries only the
// partition policy — the share of that global budget each component may use.
// Resizing the pod rescales every component automatically.
//
// In this iteration the derived soft/hard budgets are used purely for
// assessment and display (the "state" of each component and the at-a-glance
// limits); they are not written to the kernel as memory.high/memory.max.
type MemoryConfig struct {
	// Enabled is a pointer so an absent value defaults to on: the supervisor
	// measures per-component memory out of the box, even with no memory block
	// at all. Set `enabled: false` to turn the subsystem off entirely.
	Enabled *bool `yaml:"enabled,omitempty"`

	// Enforce controls the supervisor's own "choose" actions: reacting to a
	// component's memory state (warn = soft breach, fail = hard breach) with its
	// pressure_action (graceful_restart / kill / stop) and pod-pressure shedding.
	// This is a SEPARATE axis from the cgroup2 kernel primitives
	// (memory.high/max/oom.group): it is platform-independent, acts on whatever
	// per-component figure is available (exact leaf memory.current under cgroup2,
	// process RSS otherwise), and only needs a resolved global limit so budgets
	// exist. Because it does not depend on cgroups it runs — and can be tested —
	// on the macOS dev box in host mode. Pointer so an absent value defaults to
	// on; set `enforce: false` to keep tracking + state but take no action (rely
	// on the kernel backstop under cgroup2, or on alerts elsewhere).
	Enforce *bool `yaml:"enforce,omitempty"`

	// LimitEnvVar names the environment variable carrying the pod memory
	// limit in bytes. Default "MEMORY_LIMIT_BYTES". Populated via the
	// Kubernetes Downward API (resourceFieldRef: limits.memory).
	LimitEnvVar string `yaml:"limit_env_var,omitempty"`

	// LimitBytes is an explicit pod-limit override in bytes, used only as a
	// fallback when the env var is absent (and before the cgroup). It exists
	// for dev/local where there is no Downward API or cgroup to read; a real
	// pod should leave it unset and rely on the env var so the value always
	// matches the live pod spec. 0 means unset.
	LimitBytes int64 `yaml:"limit_bytes,omitempty"`

	// SupervisorReserve / CacheHeadroom are fractions of the global limit L
	// held back before partitioning. hard_pool = L*(1-reserve);
	// soft_pool = L*(1-reserve-headroom). Defaults 0.08 and 0.10.
	SupervisorReserve float64 `yaml:"supervisor_reserve,omitempty"`
	CacheHeadroom     float64 `yaml:"cache_headroom,omitempty"`

	// SampleInterval is the cheap-sample cadence. Default 5s.
	SampleInterval time.Duration `yaml:"sample_interval,omitempty"`

	// RawWindow bounds the in-memory history kept for the live view and the
	// detail-page sparkline. Default 1h.
	RawWindow time.Duration `yaml:"raw_window,omitempty"`

	// Retention bounds how long persisted NDJSON sample files are kept on
	// disk under <state_dir>/memory/. Default 72h.
	Retention time.Duration `yaml:"retention,omitempty"`

	// IncidentSamples is how many recent samples are snapshotted into an
	// incident file on an abnormal child exit. Default 60 (~5m at 5s).
	IncidentSamples int `yaml:"incident_samples,omitempty"`

	// --- Phase 2 (cgroup v2 enforcement) ---

	// PSSInterval is the slower cadence for the expensive PSS read from
	// /proc/<pid>/smaps_rollup. Default 60s; 0 disables PSS sampling entirely.
	// Only honored on Linux; ignored in host/disabled modes.
	PSSInterval time.Duration `yaml:"pss_interval,omitempty"`

	// PodPressureHigh is the container-current / global-limit ratio above which
	// the supervisor enters pod-pressure handling (the aggregate safety net).
	// Fraction of L, default 0.90.
	PodPressureHigh float64 `yaml:"pod_pressure_high,omitempty"`

	// PodPressurePSI is the pod-level PSI "some" stall ratio above which the
	// supervisor enters pod-pressure handling, as a leading indicator ahead of
	// the absolute threshold. Default 0.10.
	PodPressurePSI float64 `yaml:"pod_pressure_psi,omitempty"`

	// SustainedFor is how long a component's memory state must stay fail (over
	// the hard threshold) before the supervisor terminates it. Global, not
	// per-component — the "X seconds" of the enforce rule. Default 60s.
	SustainedFor time.Duration `yaml:"sustained_for,omitempty"`
}

// IsEnabled reports whether the memory subsystem should run. Measuring is on by
// default: a nil block or an unset enabled flag means on; only an explicit
// enabled:false turns it off.
func (this *MemoryConfig) IsEnabled() bool {
	if this == nil {
		return true
	}
	return this.Enabled == nil || *this.Enabled
}

// IsEnforcing reports whether the supervisor should take its own memory actions
// (react to the per-component memory state with pressure_action / pod-pressure
// shedding). Independent of cgroup mode; defaults to on when the subsystem is
// enabled. Actual acting additionally requires a resolved global limit so
// budgets — and therefore the warn/fail states — exist.
func (this *MemoryConfig) IsEnforcing() bool {
	if !this.IsEnabled() {
		return false
	}
	if this == nil {
		return true
	}
	return this.Enforce == nil || *this.Enforce
}

// ComponentMemoryConfig is the per-component slice of the workload budget plus
// its tracking and (phase 2) enforcement policy.
type ComponentMemoryConfig struct {
	// Share is the fraction of the workload budget this component may use.
	// Shares across components must sum to at most 1. A component without a
	// share (and without a hardlimit) is tracked but unbudgeted (no derived
	// soft/hard limit). Mutually exclusive with HardLimit.
	Share float64 `yaml:"share,omitempty"`

	// HardLimit is an ABSOLUTE hard budget in bytes (the fail threshold), an
	// alternative to the relative Share. Unlike Share it does not depend on a
	// resolved global limit — `hardlimit: 10m` means this component may use 10
	// MiB regardless of the pod size — so it is the simple way to bound one
	// component (and it makes enforcement testable on a dev box with no pod
	// limit). Mutually exclusive with Share; 0 means unset.
	HardLimit ByteSize `yaml:"hardlimit,omitempty"`

	// SoftLimit is an ABSOLUTE soft budget in bytes (the warn threshold), the
	// companion to HardLimit. When set it fixes the warn band directly; when
	// unset (but HardLimit is set) the warn band is derived just below the hard
	// cap (hard * (1 - cache_headroom)). Must be <= HardLimit. Mutually exclusive
	// with Share. 0 means unset.
	SoftLimit ByteSize `yaml:"softlimit,omitempty"`

	// Tracking enables/disables per-component sampling. Pointer so an absent
	// value defaults to on when the subsystem is enabled.
	Tracking *bool `yaml:"tracking,omitempty"`

	// MonitorOnly, when true, exempts this component from being killed for
	// memory: it is still tracked and its warn/fail state still shows, but the
	// supervisor never terminates it (and it is never shed under pod pressure).
	// This is the ONLY per-component enforcement knob; everything else (how long
	// to wait in fail before acting, etc.) is a global default. Pointer so an
	// absent value defaults to false (i.e. the component IS enforced).
	MonitorOnly *bool `yaml:"monitor_only,omitempty"`
}

// IsTracked reports whether this component should be sampled. Absent means on.
func (this *ComponentMemoryConfig) IsTracked() bool {
	if this == nil {
		return true
	}
	return this.Tracking == nil || *this.Tracking
}

// IsMonitorOnly reports whether this component is exempt from memory kills.
// Absent means false (the component is enforced).
func (this *ComponentMemoryConfig) IsMonitorOnly() bool {
	if this == nil {
		return false
	}
	return this.MonitorOnly != nil && *this.MonitorOnly
}

// StateMonitorConfig groups the two component-monitoring sub-roles.
type StateMonitorConfig struct {
	Scrape  ScrapeConfig  `yaml:"scrape,omitempty"`
	Observe ObserveConfig `yaml:"observe,omitempty"`
}

// ScrapeConfig controls the component scraper. Enabled is a pointer so an
// absent block defaults to on; set `enabled: false` to turn scraping off.
type ScrapeConfig struct {
	Enabled    *bool         `yaml:"enabled,omitempty"`
	Interval   time.Duration `yaml:"interval,omitempty"`   // default 15s
	Timeout    time.Duration `yaml:"timeout,omitempty"`    // default 5s
	Expiration time.Duration `yaml:"expiration,omitempty"` // default 1m
}

// IsEnabled reports whether scraping is on. An unset enabled flag means on.
func (this ScrapeConfig) IsEnabled() bool { return this.Enabled == nil || *this.Enabled }

// ObserveConfig controls the health-aggregation role. When Enabled, the
// supervisor stands up a statekit storage fed from its own registry on a
// ticker and serves the storage console + API under /health.
type ObserveConfig struct {
	Enabled bool `yaml:"enabled"`

	// IngestInterval is how often the supervisor snapshots its registry into
	// the store. Each snapshot updates current state and appends any new
	// transition events. Default 1s.
	IngestInterval time.Duration `yaml:"ingest_interval,omitempty"`

	// CacheMB sizes the document cache (full /state documents) in MiB.
	// Default 32. Note: storage is in-memory only — history does not survive a
	// supervisor restart.
	CacheMB int `yaml:"cache_mb,omitempty"`
}

// SupervisorConfig controls the supervisor's own HTTP server (own-state, /healthz, /metrics)
// and the registry identity emitted on every state and metric this supervisor
// publishes. MetricLabels are passed to the statekit registry as const
// labels — each key becomes a label on every metric this supervisor emits.
// The same key/value pairs also surface as label_path entries on the /state
// document so a fleet aggregator can dedupe and group across supervisors.
// A typical entry is `source: <deployment-name>`.
type SupervisorConfig struct {
	BindAddress  string            `yaml:"bind_address"`
	PublicPort   int               `yaml:"public_port,omitempty"`
	MetricLabels map[string]string `yaml:"metric_labels,omitempty"`
	Favicon      FaviconConfig     `yaml:"favicon,omitempty"`

	// BasicAuth, when enabled, gates every route on the supervisor's HTTP
	// server (including /backoffice/healthz) behind a login form.
	BasicAuth BasicAuthConfig `yaml:"basic_auth,omitempty"`
}

// FaviconConfig controls the browser tab icon served at /favicon.ico. Name is
// rendered as centered text over a status-colored background.
type FaviconConfig struct {
	Name string `yaml:"name,omitempty"`
}

// BasicAuthConfig is the optional login gate on the supervisor's HTTP server.
// When Enabled, an unauthenticated request is redirected to a plain /login
// form; submitting the configured username and password mints a session cookie
// that grants access for up to 12 hours. The cookie is a signed
// "<timestamp>.<hash>" pair (hash = sha256(timestamp|username|password)), so
// nothing is stored server-side and changing the password invalidates every
// outstanding cookie. Disabled by default so the surface stays open unless an
// operator opts in.
type BasicAuthConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`

	// Hint is an optional human-readable line shown under the login form —
	// handy for demos where the credentials aren't secret (e.g.
	// "demo login: admin / changeme"). Leave empty in production.
	Hint string `yaml:"hint,omitempty"`
}

// UpdatesConfig is the operator-facing update switch. When enabled is false,
// the supervisor does not poll an update source and runs current.txt locally.
// The remaining fields mirror remote: and are copied into the effective
// RemoteConfig during ApplyDefaults.
type UpdatesConfig struct {
	Enabled                *bool         `yaml:"enabled,omitempty"`
	BaseURL                string        `yaml:"base_url,omitempty"`
	Target                 string        `yaml:"target,omitempty"`
	PollingInterval        time.Duration `yaml:"polling_interval,omitempty"`
	Secret                 string        `yaml:"secret,omitempty"`
	SignaturePublicKeyPath string        `yaml:"signature_public_key_path,omitempty"`
}

// RemoteConfig describes the vendor's update endpoint. Per-component overrides may set any subset.
type RemoteConfig struct {
	Enabled                bool          `yaml:"-"`
	EnabledSet             bool          `yaml:"-"`
	BaseURL                string        `yaml:"base_url"`
	Target                 string        `yaml:"target"`
	PollingInterval        time.Duration `yaml:"polling_interval"`
	Secret                 string        `yaml:"secret"`
	SignaturePublicKeyPath string        `yaml:"signature_public_key_path"`
}

// ComponentConfig describes a single supervised child.
//
// Port is the well-known TCP port the child is expected to bind for its HTTP
// surface. It is required: the supervisor uses it for liveness/health
// checks, reflects it in /state, and exposes it via ${MONITOR_PORT} and
// OP_MONITOR_PORT to the child.
//
// URLs lets you override the default path layout the supervisor expects the
// child to serve (healthz, readyz, state, metrics). Unset fields fall back
// to their conventional values.
type ComponentConfig struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description,omitempty"`
	Port        int               `yaml:"port"`
	Command     string            `yaml:"command"`
	Env         map[string]string `yaml:"env,omitempty"`
	URLs        URLsConfig        `yaml:"urls,omitempty"`
	ProxyURLs   map[string]string `yaml:"proxy_urls,omitempty"`

	// Readme is an optional path to a Markdown file describing this component,
	// shown on the component's portal page. Relative paths resolve against the
	// directory holding supervisor.yml. The file is read at request time, so
	// operator edits show up without a supervisor restart.
	Readme string `yaml:"readme,omitempty"`

	// Remote overrides the top-level remote block for this component. Zero-valued
	// fields fall back to the top-level remote.
	Remote RemoteConfig `yaml:"remote,omitempty"`

	// Memory is this component's slice of the workload budget plus its
	// tracking flag. nil leaves the component tracked but unbudgeted.
	Memory *ComponentMemoryConfig `yaml:"memory,omitempty"`
}

// ExternalComponentConfig describes a component owned by another process or
// system. The supervisor does not start/stop/restart it, but proxies and
// scrapes it using URL and URLs.
type ExternalComponentConfig struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description,omitempty"`
	URL         string            `yaml:"url"`
	URLs        URLsConfig        `yaml:"urls,omitempty"`
	ProxyURLs   map[string]string `yaml:"proxy_urls,omitempty"`

	// Readme is shown on the component portal page, same as managed
	// components. Relative paths resolve against supervisor.yml.
	Readme string `yaml:"readme,omitempty"`
}

// URLsConfig describes the path layout the supervisor expects each component
// to serve on its configured port. Empty fields fall back to defaults.
type URLsConfig struct {
	Healthz     string `yaml:"healthz,omitempty"`
	Readyz      string `yaml:"readyz,omitempty"`
	State       string `yaml:"state,omitempty"`
	Metrics     string `yaml:"metrics,omitempty"`
	Escalations string `yaml:"escalations,omitempty"`
}

// LoadConfig reads and validates a supervisor YAML file, applying defaults.
//
// The file is run through the shared template engine (pkg/config) before
// being unmarshalled, so every field — including the `vars:` block, ports,
// URLs, durations — can use Go template expressions with the {{ }} and
// [[ ]] delimiters and the standard func set (default / required / env /
// add / int). The vars: block is resolved iteratively (vars can reference
// other vars or env). Templates inside YAML are fully resolved by the
// time the unmarshal sees the bytes. The ${VAR} command-template syntax in
// components.command is NOT touched here.
//
// Relative file:// URLs in any remote block are then resolved against the
// directory holding the config file so the example can ship a relative
// fixture path.
func LoadConfig(path string, opts ...config.Option) (*Config, error) {
	processed, resolvedVars, err := config.ProcessFile(path, opts...)
	if err != nil {
		return nil, fmt.Errorf("process config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(processed, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	// pkg/config strips the `vars:` block from the processed bytes (it
	// uses `vars:` purely as template-time scoping). We want the resolved
	// values to also live on Config.Vars so child env injection and manifest
	// validate_templates checks can use them.
	if len(resolvedVars) > 0 {
		if cfg.Vars == nil {
			cfg.Vars = make(map[string]any, len(resolvedVars))
		}
		for k, v := range resolvedVars {
			cfg.Vars[k] = v
		}
	}

	absConfigDir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("resolve config dir for %s: %w", path, err)
	}
	cfg.Remote.BaseURL = resolveFileURL(cfg.Remote.BaseURL, absConfigDir)
	cfg.Updates.BaseURL = resolveFileURL(cfg.Updates.BaseURL, absConfigDir)
	for i := range cfg.Components {
		cfg.Components[i].Remote.BaseURL = resolveFileURL(cfg.Components[i].Remote.BaseURL, absConfigDir)
		cfg.Components[i].Readme = resolveLocalPath(cfg.Components[i].Readme, absConfigDir)
	}
	for i := range cfg.ExternalComponents {
		cfg.ExternalComponents[i].Readme = resolveLocalPath(cfg.ExternalComponents[i].Readme, absConfigDir)
	}

	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}

	return &cfg, nil
}

// resolveLocalPath turns a relative filesystem path into an absolute one
// rooted at configDir. Empty and already-absolute paths are returned
// unchanged. Used for operator-supplied assets (e.g. component readme files)
// referenced relative to supervisor.yml.
func resolveLocalPath(raw, configDir string) string {
	if raw == "" || filepath.IsAbs(raw) {
		return raw
	}
	if abs, err := filepath.Abs(filepath.Join(configDir, raw)); err == nil {
		return abs
	}
	return raw
}

// resolveFileURL rewrites a relative file:// URL into file:///<abs path>
// against configDir. Non-file URLs and already-absolute file URLs are
// returned unchanged.
func resolveFileURL(raw, configDir string) string {
	const prefix = "file://"
	if !strings.HasPrefix(raw, prefix) {
		return raw
	}
	p := strings.TrimPrefix(raw, prefix)
	// Drop an optional "localhost" host so file://localhost/abs/path still works.
	p = strings.TrimPrefix(p, "localhost")
	if strings.HasPrefix(p, "/") {
		return raw // already absolute
	}
	abs, err := filepath.Abs(filepath.Join(configDir, p))
	if err != nil {
		return raw
	}
	return prefix + abs
}

// ApplyDefaults fills in any zero-valued fields with the documented defaults.
// Per-component remote overrides inherit from the top-level remote block.
func (this *Config) ApplyDefaults() {
	if this.StateDir == "" {
		this.StateDir = "/var/lib/go-run"
	}
	if this.StabilityTime == 0 {
		this.StabilityTime = 5 * time.Minute
	}
	if this.CrashWindow == 0 {
		this.CrashWindow = time.Minute
	}
	if this.CrashThreshold == 0 {
		this.CrashThreshold = 3
	}
	if this.ExecFailThreshold == 0 {
		this.ExecFailThreshold = 2
	}
	if this.KillGracePeriod == 0 {
		this.KillGracePeriod = 5 * time.Second
	}
	if this.VersionFolderRetention == 0 {
		this.VersionFolderRetention = 2
	}
	// RejectExpiry has NO default — unset means "no autonomous clearing,
	// rejections stay active forever". Operators who want auto-clearing
	// set an explicit duration (e.g. reject_expiry: 1h).
	if this.LogMaxSize == 0 {
		this.LogMaxSize = 10 * 1024 * 1024 // 10 MiB per active file.
	}
	// LogMaxFiles defaults to 5 history generations. We can't distinguish
	// "explicit 0" from "unset" with int; operators who want history off
	// pass a negative number, which we map to 0 in the rotatingFile.
	if this.LogMaxFiles == 0 {
		this.LogMaxFiles = 5
	} else if this.LogMaxFiles < 0 {
		this.LogMaxFiles = 0
	}
	if this.Supervisor.BindAddress == "" {
		this.Supervisor.BindAddress = "127.0.0.1:9090"
	}
	if this.Supervisor.Favicon.Name == "" {
		this.Supervisor.Favicon.Name = "GR"
	}
	this.Remote = applyUpdates(this.Remote, this.Updates)
	if this.Remote.Target == "" {
		this.Remote.Target = "required.txt"
	}
	if this.Remote.PollingInterval == 0 {
		this.Remote.PollingInterval = time.Minute
	}
	if this.StateMonitor.Scrape.Interval == 0 {
		this.StateMonitor.Scrape.Interval = 15 * time.Second
	}
	if this.StateMonitor.Scrape.Timeout == 0 {
		this.StateMonitor.Scrape.Timeout = 5 * time.Second
	}
	if this.StateMonitor.Scrape.Expiration == 0 {
		this.StateMonitor.Scrape.Expiration = time.Minute
	}
	if this.StateMonitor.Observe.Enabled {
		if this.StateMonitor.Observe.IngestInterval == 0 {
			this.StateMonitor.Observe.IngestInterval = time.Second
		}
		if this.StateMonitor.Observe.CacheMB == 0 {
			this.StateMonitor.Observe.CacheMB = 32
		}
	}

	this.applyMemoryDefaults()

	for i := range this.Components {
		this.Components[i].Remote = mergeRemote(this.Remote, this.Components[i].Remote)
		if this.Components[i].Remote.BaseURL != "" && this.Updates.Enabled == nil {
			this.Components[i].Remote.Enabled = true
		}
		this.Components[i].URLs = applyURLDefaults(this.Components[i].URLs)
	}
	for i := range this.ExternalComponents {
		this.ExternalComponents[i].URLs = applyExternalURLDefaults(this.ExternalComponents[i].URLs)
	}
}

// applyMemoryDefaults fills the memory subsystem defaults when the block is
// present. A nil block is left nil (subsystem off). Per-component defaults are
// only material when the subsystem is enabled.
func (this *Config) applyMemoryDefaults() {
	// Instantiate an empty block when absent so measuring is on by default and
	// the monitor never dereferences a nil config. An explicit enabled:false
	// still resolves the subsystem to disabled via IsEnabled.
	if this.Memory == nil {
		this.Memory = &MemoryConfig{}
	}
	m := this.Memory
	if m.LimitEnvVar == "" {
		m.LimitEnvVar = "MEMORY_LIMIT_BYTES"
	}
	if m.SupervisorReserve == 0 {
		m.SupervisorReserve = 0.08
	}
	if m.CacheHeadroom == 0 {
		m.CacheHeadroom = 0.10
	}
	if m.SampleInterval == 0 {
		m.SampleInterval = 5 * time.Second
	}
	if m.RawWindow == 0 {
		m.RawWindow = time.Hour
	}
	if m.Retention == 0 {
		m.Retention = 72 * time.Hour
	}
	if m.IncidentSamples == 0 {
		m.IncidentSamples = 60
	}
	if m.PSSInterval == 0 {
		m.PSSInterval = 60 * time.Second
	}
	if m.PodPressureHigh == 0 {
		m.PodPressureHigh = 0.90
	}
	if m.PodPressurePSI == 0 {
		m.PodPressurePSI = 0.10
	}
	if m.SustainedFor == 0 {
		m.SustainedFor = 60 * time.Second
	}
}

// applyURLDefaults fills in any unset URL paths with the conventional values.
func applyURLDefaults(u URLsConfig) URLsConfig {
	if u.Healthz == "" {
		u.Healthz = "/healthz"
	}
	if u.Readyz == "" {
		u.Readyz = "/readyz"
	}
	if u.State == "" {
		u.State = "/state"
	}
	if u.Metrics == "" {
		u.Metrics = "/metrics"
	}
	if u.Escalations == "" {
		u.Escalations = "/escalations"
	}
	return u
}

// applyExternalURLDefaults fills only reachability defaults. State, metrics,
// and escalations are opt-in for external components because many external
// apps will not expose statekit/Prometheus endpoints.
func applyExternalURLDefaults(u URLsConfig) URLsConfig {
	if u.Healthz == "" {
		u.Healthz = "/healthz"
	}
	if u.Readyz == "" {
		u.Readyz = "/readyz"
	}
	return u
}

// Validate checks that the resolved config is internally consistent.
func (this *Config) Validate() error {
	if name := strings.TrimSpace(this.Supervisor.Favicon.Name); len([]rune(name)) > 2 {
		return fmt.Errorf("supervisor.favicon.name must be at most two characters")
	}
	if ba := this.Supervisor.BasicAuth; ba.Enabled {
		if ba.Username == "" || ba.Password == "" {
			return fmt.Errorf("supervisor.basic_auth: username and password are required when enabled")
		}
	}
	if err := this.validateMemory(); err != nil {
		return err
	}
	if len(this.Components) == 0 && len(this.ExternalComponents) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(this.Components))
	portSeen := make(map[int]string, len(this.Components))
	for i, c := range this.Components {
		if c.Name == "" {
			return fmt.Errorf("components[%d]: name is required", i)
		}
		if seen[c.Name] {
			return fmt.Errorf("components[%d]: duplicate name %q", i, c.Name)
		}
		seen[c.Name] = true
		if c.Command == "" {
			return fmt.Errorf("components[%q]: command is required", c.Name)
		}
		if c.Port <= 0 || c.Port > 65535 {
			return fmt.Errorf("components[%q]: port is required and must be 1..65535 (got %d)", c.Name, c.Port)
		}
		if other, ok := portSeen[c.Port]; ok {
			return fmt.Errorf("components[%q]: port %d already used by component %q", c.Name, c.Port, other)
		}
		if c.Remote.Enabled && c.Remote.BaseURL == "" {
			return fmt.Errorf("components[%q]: updates are enabled but remote.base_url is empty", c.Name)
		}
		if err := validateProxyURLs(c.ProxyURLs); err != nil {
			return fmt.Errorf("components[%q]: proxy_urls: %w", c.Name, err)
		}
		portSeen[c.Port] = c.Name
	}
	for i, c := range this.ExternalComponents {
		if c.Name == "" {
			return fmt.Errorf("external_components[%d]: name is required", i)
		}
		if seen[c.Name] {
			return fmt.Errorf("external_components[%d]: duplicate name %q", i, c.Name)
		}
		seen[c.Name] = true
		if c.URL == "" {
			return fmt.Errorf("external_components[%q]: url is required", c.Name)
		}
		if err := validateHTTPBaseURL(c.URL); err != nil {
			return fmt.Errorf("external_components[%q]: url: %w", c.Name, err)
		}
		if err := validateProxyURLs(c.ProxyURLs); err != nil {
			return fmt.Errorf("external_components[%q]: proxy_urls: %w", c.Name, err)
		}
	}
	return nil
}

// validateMemory checks the memory subsystem's partition policy is internally
// consistent: reserves leave a positive budget and component shares do not
// over-subscribe the workload pool. A small epsilon absorbs float rounding.
func (this *Config) validateMemory() error {
	m := this.Memory
	if !m.IsEnabled() {
		return nil
	}
	const epsilon = 1e-9
	if m.SupervisorReserve < 0 || m.CacheHeadroom < 0 {
		return fmt.Errorf("memory: supervisor_reserve and cache_headroom must be non-negative")
	}
	if m.SupervisorReserve+m.CacheHeadroom >= 1 {
		return fmt.Errorf("memory: supervisor_reserve + cache_headroom must be < 1 (got %.3f)",
			m.SupervisorReserve+m.CacheHeadroom)
	}
	var sumShares float64
	for _, c := range this.Components {
		if c.Memory == nil {
			continue
		}
		if c.Memory.Share < 0 {
			return fmt.Errorf("components[%q]: memory.share must be non-negative", c.Name)
		}
		if c.Memory.HardLimit < 0 || c.Memory.SoftLimit < 0 {
			return fmt.Errorf("components[%q]: memory.hardlimit/softlimit must be non-negative", c.Name)
		}
		if c.Memory.Share > 0 && (c.Memory.HardLimit > 0 || c.Memory.SoftLimit > 0) {
			return fmt.Errorf("components[%q]: memory.share is mutually exclusive with hardlimit/softlimit", c.Name)
		}
		if c.Memory.SoftLimit > 0 && c.Memory.HardLimit > 0 && c.Memory.SoftLimit > c.Memory.HardLimit {
			return fmt.Errorf("components[%q]: memory.softlimit (%d) must be <= hardlimit (%d)",
				c.Name, int64(c.Memory.SoftLimit), int64(c.Memory.HardLimit))
		}
		sumShares += c.Memory.Share
	}
	if m.PodPressureHigh < 0 || m.PodPressureHigh > 1 {
		return fmt.Errorf("memory: pod_pressure_high must be in [0,1] (got %.3f)", m.PodPressureHigh)
	}
	if m.PodPressurePSI < 0 || m.PodPressurePSI > 1 {
		return fmt.Errorf("memory: pod_pressure_psi must be in [0,1] (got %.3f)", m.PodPressurePSI)
	}
	if sumShares > 1+epsilon {
		return fmt.Errorf("memory: component shares must sum to <= 1 (got %.3f)", sumShares)
	}
	return nil
}

func validateProxyURLs(proxyURLs map[string]string) error {
	for key, spec := range proxyURLs {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("key is required")
		}
		if strings.Contains(key, "/") {
			return fmt.Errorf("key %q must not contain /", key)
		}
		if strings.TrimSpace(spec) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	return nil
}

func validateHTTPBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("must use http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("host is required")
	}
	return nil
}

// mergeRemote returns the effective remote config: each zero-valued field in
// override falls back to the corresponding field in base.
func mergeRemote(base, override RemoteConfig) RemoteConfig {
	out := override
	if !out.EnabledSet {
		out.EnabledSet = base.EnabledSet
	}
	if !out.Enabled {
		out.Enabled = base.Enabled
	}
	if out.BaseURL == "" {
		out.BaseURL = base.BaseURL
	}
	if out.Target == "" {
		out.Target = base.Target
	}
	if out.PollingInterval == 0 {
		out.PollingInterval = base.PollingInterval
	}
	if out.Secret == "" {
		out.Secret = base.Secret
	}
	if out.SignaturePublicKeyPath == "" {
		out.SignaturePublicKeyPath = base.SignaturePublicKeyPath
	}
	return out
}

func applyUpdates(remote RemoteConfig, updates UpdatesConfig) RemoteConfig {
	if updates.BaseURL != "" {
		remote.BaseURL = updates.BaseURL
	}
	if updates.Target != "" {
		remote.Target = updates.Target
	}
	if updates.PollingInterval != 0 {
		remote.PollingInterval = updates.PollingInterval
	}
	if updates.Secret != "" {
		remote.Secret = updates.Secret
	}
	if updates.SignaturePublicKeyPath != "" {
		remote.SignaturePublicKeyPath = updates.SignaturePublicKeyPath
	}
	if updates.Enabled != nil {
		remote.Enabled = *updates.Enabled
		remote.EnabledSet = true
	} else if remote.BaseURL != "" {
		remote.Enabled = true
	}
	return remote
}
