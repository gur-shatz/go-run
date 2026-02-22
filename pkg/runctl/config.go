package runctl

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gur-shatz/go-run/pkg/config"
)

// Config is the top-level runctl.yaml configuration.
type Config struct {
	Vars    map[string]string       `yaml:"vars,omitempty"`
	API     APIConfig               `yaml:"api"`
	LogsDir string                  `yaml:"logs_dir,omitempty"` // directory for auto-generated log files
	Targets map[string]TargetConfig `yaml:"targets"`

	// ResolvedVars holds all resolved template variables (vars section + env).
	// Populated by LoadConfig, not from YAML.
	ResolvedVars map[string]string `yaml:"-"`
}

// APIConfig controls the HTTP API server.
type APIConfig struct {
	Port int `yaml:"port"`
}

// TargetConfig describes a single managed target.
type TargetConfig struct {
	Config  string            `yaml:"config"`          // path to config file (relative to runctl.yaml dir)
	Enabled *bool             `yaml:"enabled,omitempty"`
	Links   []Link            `yaml:"links,omitempty"`
	Vars    map[string]string `yaml:"vars,omitempty"` // per-target template vars (override global vars)

	// Logs is populated internally from Config.LogsDir — not user-configurable.
	Logs *LogsConfig `yaml:"-"`
}

// Link is a named URL or file path associated with a target.
type Link struct {
	Name        string `yaml:"name"              json:"name"`
	URL         string `yaml:"url,omitempty"      json:"url,omitempty"`
	File        string `yaml:"file,omitempty"     json:"file,omitempty"`
	ResolvedURL string `yaml:"-"                  json:"resolved_url,omitempty"`
}

// LogsConfig holds resolved log file paths for a target.
// Populated internally from Config.LogsDir.
// Files are separated by stage: build (compile/exec steps) and run (managed process).
type LogsConfig struct {
	Build string `json:"build,omitempty"` // build stage log file
	Run   string `json:"run,omitempty"`   // run stage log file
}

// IsEnabled returns whether the target should start on launch (default: true).
func (this TargetConfig) IsEnabled() bool {
	if this.Enabled == nil {
		return true
	}
	return *this.Enabled
}

// LoadConfig reads and parses a runctl.yaml file.
// Template variables from the vars: section are resolved using Go templates,
// then set in the process environment (if not already present) so child
// configs can access them.
func LoadConfig(path string) (*Config, error) {
	data, resolvedVars, err := config.ProcessFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.ResolvedVars = resolvedVars

	// Set resolved vars in environment so child processes can access them.
	for k, v := range resolvedVars {
		os.Setenv(k, v)
	}

	// Resolve per-target vars using global vars as template data.
	envMap := environMap()
	for name, t := range cfg.Targets {
		if len(t.Vars) == 0 {
			continue
		}
		td := make(map[string]any, len(resolvedVars))
		for k, v := range resolvedVars {
			td[k] = v
		}

		resolved := make(map[string]string, len(t.Vars))
		for k, expr := range t.Vars {
			val, err := config.ResolveExpr(expr, td, envMap)
			if err != nil {
				return nil, fmt.Errorf("target %q: resolve var %q: %w", name, k, err)
			}
			resolved[k] = val
		}
		t.Vars = resolved
		cfg.Targets[name] = t

		// Set target vars in environment (overrides global vars).
		for k, v := range resolved {
			os.Setenv(k, v)
		}
	}

	// Resolve relative logs_dir against the config file's directory
	if cfg.LogsDir != "" && !filepath.IsAbs(cfg.LogsDir) {
		cfg.LogsDir = filepath.Join(filepath.Dir(path), cfg.LogsDir)
	}

	configDir := filepath.Dir(path)

	// Resolve relative file paths in links
	for name, t := range cfg.Targets {
		for i, link := range t.Links {
			if link.File != "" && !filepath.IsAbs(link.File) {
				t.Links[i].File = filepath.Join(configDir, link.File)
			}
		}
		cfg.Targets[name] = t
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// normalizeTargetName converts a target name to a safe filename component.
// It lowercases the name and replaces any non-alphanumeric characters (except
// hyphens and underscores) with underscores.
var reUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

func normalizeTargetName(name string) string {
	return reUnsafe.ReplaceAllString(strings.ToLower(name), "_")
}

// Validate checks the config for required fields and sets defaults.
func (this *Config) Validate() error {
	if this.API.Port == 0 {
		this.API.Port = 9100
	}
	if len(this.Targets) == 0 {
		return fmt.Errorf("at least one target is required")
	}
	for name, t := range this.Targets {
		if t.Config == "" {
			return fmt.Errorf("target %q: config is required", name)
		}

		// Validate links: each must have exactly one of url or file
		for i, link := range t.Links {
			hasURL := link.URL != ""
			hasFile := link.File != ""
			if hasURL && hasFile {
				return fmt.Errorf("target %q: link %d (%q): cannot specify both url and file", name, i, link.Name)
			}
			if !hasURL && !hasFile {
				return fmt.Errorf("target %q: link %d (%q): must specify either url or file", name, i, link.Name)
			}
		}

		// Populate log paths from logs_dir
		if this.LogsDir != "" {
			norm := normalizeTargetName(name)
			t.Logs = &LogsConfig{
				Build: filepath.Join(this.LogsDir, norm+".build.log"),
				Run:   filepath.Join(this.LogsDir, norm+".run.log"),
			}
			this.Targets[name] = t
		}
	}
	return nil
}

// environMap returns the current environment as a key→value map.
func environMap() map[string]string {
	env := os.Environ()
	m := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	return m
}

// DefaultConfigYAML is the commented starter YAML for `runctl init`.
//
//go:embed runctl.default.yaml
var DefaultConfigYAML string
