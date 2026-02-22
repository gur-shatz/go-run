package runctl

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Controller manages multiple targets and exposes an HTTP API.
type Controller struct {
	cfg     Config
	baseDir string
	targets map[string]*target
	mu      sync.RWMutex
}

// New creates a Controller from the given config.
// baseDir is the directory containing runctl.yaml (used to resolve relative target dirs).
func New(cfg Config, baseDir string) (*Controller, error) {
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve base dir: %w", err)
	}

	// Ensure logs_dir exists if configured
	if cfg.LogsDir != "" {
		logsDir := cfg.LogsDir
		if !filepath.IsAbs(logsDir) {
			logsDir = filepath.Join(absBase, logsDir)
		}
		if err := os.MkdirAll(logsDir, 0755); err != nil {
			return nil, fmt.Errorf("create logs_dir %s: %w", logsDir, err)
		}
	}

	ctrl := &Controller{
		cfg:     cfg,
		baseDir: absBase,
		targets: make(map[string]*target, len(cfg.Targets)),
	}

	for name, tcfg := range cfg.Targets {
		// Merge global vars with per-target vars (target wins on conflict)
		parentVars := cfg.ResolvedVars
		if len(tcfg.Vars) > 0 {
			parentVars = make(map[string]string, len(cfg.ResolvedVars)+len(tcfg.Vars))
			for k, v := range cfg.ResolvedVars {
				parentVars[k] = v
			}
			for k, v := range tcfg.Vars {
				parentVars[k] = v
			}
		}
		ctrl.targets[name] = newTarget(name, tcfg, absBase, parentVars)
	}

	return ctrl, nil
}

// StartTargets launches all enabled targets.
func (this *Controller) StartTargets() {
	this.mu.RLock()
	defer this.mu.RUnlock()

	for name, t := range this.targets {
		if t.enabled {
			if err := t.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "[runctl] Warning: failed to start %s: %v\n", name, err)
			}
		}
	}
}

// StartTargetsFiltered launches only the named targets.
// If names is empty, it starts all enabled targets (same as StartTargets).
func (this *Controller) StartTargetsFiltered(names []string) {
	if len(names) == 0 {
		this.StartTargets()
		return
	}

	this.mu.RLock()
	defer this.mu.RUnlock()

	filter := make(map[string]bool, len(names))
	for _, n := range names {
		filter[n] = true
	}

	for name, t := range this.targets {
		if filter[name] {
			if err := t.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "[runctl] Warning: failed to start %s: %v\n", name, err)
			}
		}
	}
}

// StopTargets gracefully stops all targets (SIGTERM → 5s → SIGKILL).
func (this *Controller) StopTargets() {
	this.mu.RLock()
	defer this.mu.RUnlock()

	for _, t := range this.targets {
		t.Stop()
	}
}

// KillTargets immediately kills all target processes (SIGKILL).
func (this *Controller) KillTargets() {
	this.mu.RLock()
	defer this.mu.RUnlock()

	for _, t := range this.targets {
		t.Kill()
	}
}

// StartTarget starts a target by name.
func (this *Controller) StartTarget(name string) error {
	this.mu.RLock()
	t, ok := this.targets[name]
	this.mu.RUnlock()
	if !ok {
		return fmt.Errorf("target %q not found", name)
	}
	return t.Start()
}

// StopTarget stops a target by name.
func (this *Controller) StopTarget(name string) error {
	this.mu.RLock()
	t, ok := this.targets[name]
	this.mu.RUnlock()
	if !ok {
		return fmt.Errorf("target %q not found", name)
	}
	t.Stop()
	return nil
}

// BuildTarget triggers a rebuild + restart for a target.
func (this *Controller) BuildTarget(name string) error {
	this.mu.RLock()
	t, ok := this.targets[name]
	this.mu.RUnlock()
	if !ok {
		return fmt.Errorf("target %q not found", name)
	}
	t.Build()
	return nil
}

// StartExec starts just the managed process (no rebuild).
func (this *Controller) StartExec(name string) error {
	this.mu.RLock()
	t, ok := this.targets[name]
	this.mu.RUnlock()
	if !ok {
		return fmt.Errorf("target %q not found", name)
	}
	t.StartExec()
	return nil
}

// StopExec stops just the managed process (keep watcher running).
func (this *Controller) StopExec(name string) error {
	this.mu.RLock()
	t, ok := this.targets[name]
	this.mu.RUnlock()
	if !ok {
		return fmt.Errorf("target %q not found", name)
	}
	t.StopExec()
	return nil
}

// RestartTarget stops and re-starts a target.
func (this *Controller) RestartTarget(name string) error {
	this.mu.RLock()
	t, ok := this.targets[name]
	this.mu.RUnlock()
	if !ok {
		return fmt.Errorf("target %q not found", name)
	}
	t.Stop()
	return t.Start()
}

// EnableTarget enables a target and starts it.
func (this *Controller) EnableTarget(name string) error {
	this.mu.RLock()
	t, ok := this.targets[name]
	this.mu.RUnlock()
	if !ok {
		return fmt.Errorf("target %q not found", name)
	}
	t.mu.Lock()
	t.enabled = true
	t.mu.Unlock()
	return t.Start()
}

// DisableTarget stops a target and disables it.
func (this *Controller) DisableTarget(name string) error {
	this.mu.RLock()
	t, ok := this.targets[name]
	this.mu.RUnlock()
	if !ok {
		return fmt.Errorf("target %q not found", name)
	}
	t.Stop()
	t.mu.Lock()
	t.enabled = false
	t.mu.Unlock()
	return nil
}

// AllowedFilePaths returns the set of absolute file paths from link configs.
// Used to restrict which files the /api/file endpoint can serve.
func (this *Controller) AllowedFilePaths() map[string]bool {
	this.mu.RLock()
	defer this.mu.RUnlock()

	allowed := make(map[string]bool)
	for _, t := range this.targets {
		for _, link := range t.tcfg.Links {
			if link.File != "" {
				allowed[link.File] = true
			}
		}
	}
	return allowed
}

// Status returns the status of all targets.
func (this *Controller) Status() []TargetStatus {
	this.mu.RLock()
	defer this.mu.RUnlock()

	statuses := make([]TargetStatus, 0, len(this.targets))
	for _, t := range this.targets {
		statuses = append(statuses, t.Status())
	}
	return statuses
}

// TargetStatus returns the status of a single target.
func (this *Controller) TargetStatus(name string) (*TargetStatus, error) {
	this.mu.RLock()
	t, ok := this.targets[name]
	this.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("target %q not found", name)
	}
	s := t.Status()
	return &s, nil
}
