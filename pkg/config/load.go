package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const defaultConfigFilename = "config.yml"

// LoadOption is a functional option for Load.
type LoadOption func(*loadOptions)

type loadOptions struct {
	env   map[string]string
	quiet bool
}

// WithLoadEnv sets the environment variables to use for template substitution.
// If not provided, os.Environ() is used.
func WithLoadEnv(env map[string]string) LoadOption {
	return func(o *loadOptions) {
		o.env = env
	}
}

// WithQuiet suppresses info-level logging during config loading.
func WithQuiet() LoadOption {
	return func(o *loadOptions) {
		o.quiet = true
	}
}

// LoadQuiet is a convenience wrapper for Load with the WithQuiet option.
func LoadQuiet(path string) (O, map[string]string, error) {
	return Load(path, WithQuiet())
}

// Load reads configuration from a YAML file.
// If path is provided, it must exist (no fallback).
// If path is empty, tries to find config.yml in:
//  1. Current working directory
//  2. Executable directory
//
// If no config file is found, returns empty configuration.
// Returns the config O, resolved vars map, and any error.
func Load(path string, opts ...LoadOption) (O, map[string]string, error) {
	options := loadOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	env := options.env
	if env == nil {
		env = environMap()
	}

	if path != "" {
		return loadFromFile(path, env, options.quiet)
	}

	// Try current working directory
	if cwdPath, err := findConfigInCWD(); err == nil {
		return loadFromFile(cwdPath, env, options.quiet)
	}

	// Try executable directory
	if exePath, err := findConfigInExeDir(); err == nil {
		return loadFromFile(exePath, env, options.quiet)
	}

	return O{}, nil, nil
}

// loadFromFile reads and parses a config file, returning config, resolved vars, and error.
func loadFromFile(path string, env map[string]string, quiet bool) (O, map[string]string, error) {
	// Look for vars.yml in the same directory as config file
	configDir := filepath.Dir(path)
	varsPath := filepath.Join(configDir, "vars.yml")
	env = loadVarsFile(varsPath, env)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	// Process template substitution
	processed, resolvedVars, err := Process(data, WithEnv(env))
	if err != nil {
		return nil, nil, fmt.Errorf("template substitution failed: %w", err)
	}

	var cfg O
	if err := yaml.Unmarshal(processed, &cfg); err != nil {
		return nil, nil, err
	}

	// Remove vars section from final config (Process already removes it from bytes,
	// but if yaml.Unmarshal re-parses, ensure it's gone)
	delete(cfg, varsKey)

	return cfg, resolvedVars, nil
}

// loadVarsFile reads a vars.yml file and merges its values into the env map.
// Real environment variables take precedence over vars file values.
func loadVarsFile(path string, env map[string]string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		// vars.yml is optional
		return env
	}

	var vars map[string]any
	if err := yaml.Unmarshal(data, &vars); err != nil {
		return env
	}

	merged := make(map[string]string)
	for k, v := range vars {
		merged[k] = fmt.Sprintf("%v", v)
	}
	for k, v := range env {
		merged[k] = v
	}

	return merged
}

// findConfigInCWD looks for config file in current working directory.
func findConfigInCWD() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	path := filepath.Join(cwd, defaultConfigFilename)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}

	return path, nil
}

// ProcessVariables is kept for backward compatibility.
// The main substitution now happens in Process on the raw YAML string.
func ProcessVariables(cfg O) (O, error) {
	if cfg == nil {
		return O{}, nil
	}
	delete(cfg, varsKey)
	return cfg, nil
}

// findConfigInExeDir looks for config file in executable directory.
func findConfigInExeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}

	exeDir := filepath.Dir(exe)
	path := filepath.Join(exeDir, defaultConfigFilename)
	if _, err := os.Stat(path); err != nil {
		return "", err
	}

	return path, nil
}
