package supervisor

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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

	Supervisor SupervisorConfig `yaml:"supervisor"`
	Remote     RemoteConfig     `yaml:"remote"`

	Components []ComponentConfig `yaml:"components"`
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
	MetricLabels map[string]string `yaml:"metric_labels,omitempty"`
}

// RemoteConfig describes the vendor's update endpoint. Per-component overrides may set any subset.
type RemoteConfig struct {
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
	Name    string            `yaml:"name"`
	Description string 		  `yaml:"description,omitempty"`
	Port    int               `yaml:"port"`
	Command string            `yaml:"command"`
	Env     map[string]string `yaml:"env,omitempty"`
	URLs    URLsConfig        `yaml:"urls,omitempty"`

	// Remote overrides the top-level remote block for this component. Zero-valued
	// fields fall back to the top-level remote.
	Remote RemoteConfig `yaml:"remote,omitempty"`
}

// URLsConfig describes the path layout the supervisor expects each component
// to serve on its configured port. Empty fields fall back to defaults.
type URLsConfig struct {
	Healthz string `yaml:"healthz,omitempty"`
	Readyz  string `yaml:"readyz,omitempty"`
	State   string `yaml:"state,omitempty"`
	Metrics string `yaml:"metrics,omitempty"`
}

// LoadConfig reads and validates a supervisor YAML file, applying defaults.
// Relative file:// URLs in any remote block are resolved against the
// directory holding the config file so the example can ship a relative
// fixture path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	absConfigDir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("resolve config dir for %s: %w", path, err)
	}
	cfg.Remote.BaseURL = resolveFileURL(cfg.Remote.BaseURL, absConfigDir)
	for i := range cfg.Components {
		cfg.Components[i].Remote.BaseURL = resolveFileURL(cfg.Components[i].Remote.BaseURL, absConfigDir)
	}

	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}

	return &cfg, nil
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
	if this.Supervisor.BindAddress == "" {
		this.Supervisor.BindAddress = "127.0.0.1:9090"
	}
	if this.Remote.Target == "" {
		this.Remote.Target = "required.txt"
	}
	if this.Remote.PollingInterval == 0 {
		this.Remote.PollingInterval = time.Minute
	}

	for i := range this.Components {
		this.Components[i].Remote = mergeRemote(this.Remote, this.Components[i].Remote)
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
	return u
}

// Validate checks that the resolved config is internally consistent.
func (this *Config) Validate() error {
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
		portSeen[c.Port] = c.Name
		if c.Remote.BaseURL == "" {
			return fmt.Errorf("components[%q]: remote.base_url is required (set top-level remote.base_url or override per component)", c.Name)
		}
	}
	return nil
}

// mergeRemote returns the effective remote config: each zero-valued field in
// override falls back to the corresponding field in base.
func mergeRemote(base, override RemoteConfig) RemoteConfig {
	out := override
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
