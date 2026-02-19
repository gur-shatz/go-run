package runctl

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level runctl.yaml configuration.
type Config struct {
	Env     map[string]string       `yaml:"env,omitempty"`
	API     APIConfig               `yaml:"api"`
	LogsDir string                  `yaml:"logs_dir,omitempty"` // directory for auto-generated log files
	Targets map[string]TargetConfig `yaml:"targets"`
}

// APIConfig controls the HTTP API server.
type APIConfig struct {
	Port int `yaml:"port"`
}

// TargetConfig describes a single managed target.
type TargetConfig struct {
	Type    string `yaml:"type,omitempty"` // "execrun" (default) or "gorun"
	Config  string `yaml:"config"`        // path to config file (relative to runctl.yaml dir)
	Enabled *bool  `yaml:"enabled,omitempty"`
	Links   []Link `yaml:"links,omitempty"`

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

// EffectiveType returns the target type, defaulting to "execrun".
func (this TargetConfig) EffectiveType() string {
	if this.Type == "" {
		return "execrun"
	}
	return this.Type
}

// IsEnabled returns whether the target should start on launch (default: true).
func (this TargetConfig) IsEnabled() bool {
	if this.Enabled == nil {
		return true
	}
	return *this.Enabled
}

// LoadConfig reads and parses a runctl.yaml file.
// It performs a two-pass parse: first to extract env vars and set them,
// then re-expand and re-parse the full config with env vars applied.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// First pass: expand existing env vars and extract the env block
	expanded := os.ExpandEnv(string(data))
	var envOnly struct {
		Env map[string]string `yaml:"env"`
	}
	if err := yaml.Unmarshal([]byte(expanded), &envOnly); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	// Set env vars so they're available for the second expansion
	for k, v := range envOnly.Env {
		os.Setenv(k, v)
	}

	// Second pass: re-expand with new env vars and parse full config
	expanded = os.ExpandEnv(string(data))
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
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

		switch t.EffectiveType() {
		case "execrun", "gorun":
			// valid
		default:
			return fmt.Errorf("target %q: unknown type %q (must be \"execrun\" or \"gorun\")", name, t.Type)
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

// DefaultConfigYAML is the commented starter YAML for `runctl init`.
//
//go:embed runctl.default.yaml
var DefaultConfigYAML string
