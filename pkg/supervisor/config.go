package supervisor

import (
	_ "embed"
	"fmt"
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

	// Readme is an optional path to a Markdown file describing this component,
	// shown on the component's portal page. Relative paths resolve against the
	// directory holding supervisor.yml. The file is read at request time, so
	// operator edits show up without a supervisor restart.
	Readme string `yaml:"readme,omitempty"`

	// Remote overrides the top-level remote block for this component. Zero-valued
	// fields fall back to the top-level remote.
	Remote RemoteConfig `yaml:"remote,omitempty"`
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
func LoadConfig(path string) (*Config, error) {
	processed, resolvedVars, err := config.ProcessFile(path)
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

	for i := range this.Components {
		this.Components[i].Remote = mergeRemote(this.Remote, this.Components[i].Remote)
		if this.Components[i].Remote.BaseURL != "" && this.Updates.Enabled == nil {
			this.Components[i].Remote.Enabled = true
		}
		this.Components[i].URLs = applyURLDefaults(this.Components[i].URLs)
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
	if len(this.Components) == 0 {
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
		portSeen[c.Port] = c.Name
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
