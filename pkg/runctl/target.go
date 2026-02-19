package runctl

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gur-shatz/go-run/pkg/execrun"
	"github.com/gur-shatz/go-run/pkg/gorun"
)

// TargetState represents the current state of a target.
type TargetState string

const (
	StateIdle     TargetState = "idle"
	StateStarting TargetState = "starting"
	StateRunning  TargetState = "running"
	StateStopped  TargetState = "stopped"
	StateError    TargetState = "error"
	StateExited   TargetState = "exited"
)

// TargetStatus is the JSON-serializable status of a target.
type TargetStatus struct {
	Name    string      `json:"name"`
	Type    string      `json:"type"`
	HasBuild bool       `json:"has_build"`
	HasRun   bool       `json:"has_run"`
	State   TargetState `json:"state"`
	Enabled bool        `json:"enabled"`
	PID     int         `json:"pid,omitempty"`

	LastExecTime     *time.Time `json:"last_exec_time,omitempty"`
	LastExecDuration *float64   `json:"last_exec_duration_secs,omitempty"`
	LastExecResult   string     `json:"last_exec_result,omitempty"`
	LastExecError    string     `json:"last_exec_error,omitempty"`

	LastStartTime *time.Time `json:"last_start_time,omitempty"`
	RestartCount  int        `json:"restart_count"`
	BuildCount    int        `json:"build_count"`

	Links []Link      `json:"links,omitempty"`
	Logs  *LogsConfig `json:"logs,omitempty"`
}

// target wraps a target config and manages its lifecycle.
type target struct {
	name    string
	tcfg    TargetConfig
	rootDir string // absolute path to target working directory
	hasBuild bool
	hasRun   bool

	mu      sync.Mutex
	state   TargetState
	enabled bool
	cancel  context.CancelFunc
	pid     int

	lastExecTime     *time.Time
	lastExecDuration *float64
	lastExecResult   string
	lastExecError    string
	lastStartTime    *time.Time
	restartCount     int
	buildCount       int

	buildTrigger chan struct{}
	execStop     chan struct{}
	execStart    chan struct{}
}

func newTarget(name string, tcfg TargetConfig, baseDir string) *target {
	dir := filepath.Dir(tcfg.Config)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}

	return &target{
		name:         name,
		tcfg:         tcfg,
		rootDir:      dir,
		hasBuild:     true, // default; refined for execrun targets after config load
		hasRun:       true,
		state:        StateIdle,
		enabled:      tcfg.IsEnabled(),
		buildTrigger: make(chan struct{}, 1),
		execStop:     make(chan struct{}, 1),
		execStart:    make(chan struct{}, 1),
	}
}

// Start launches the target's run loop in a goroutine.
func (this *target) Start() error {
	this.mu.Lock()
	if this.state == StateRunning || this.state == StateStarting {
		this.mu.Unlock()
		return fmt.Errorf("target %q is already running", this.name)
	}
	this.state = StateStarting
	this.mu.Unlock()

	switch this.tcfg.EffectiveType() {
	case "gorun":
		return this.startGorun()
	default:
		return this.startExecrun()
	}
}

func (this *target) startExecrun() error {
	configFile := filepath.Base(this.tcfg.Config)
	configPath := filepath.Join(this.rootDir, configFile)
	ecfg, err := execrun.LoadConfig(configPath)
	if err != nil {
		this.mu.Lock()
		this.state = StateError
		this.lastExecError = err.Error()
		this.mu.Unlock()
		return fmt.Errorf("target %q: load config: %w", this.name, err)
	}

	this.hasBuild = true
	this.hasRun = !ecfg.IsBuildOnly()

	ctx, cancel := context.WithCancel(context.Background())
	this.mu.Lock()
	this.cancel = cancel
	this.mu.Unlock()

	var closers []io.Closer
	var buildLog, runLog io.Writer = os.Stdout, os.Stdout
	if this.tcfg.Logs != nil {
		var err error
		buildLog, err = openLogFile(this.tcfg.Logs.Build, os.Stdout, &closers)
		if err != nil {
			cancel()
			return fmt.Errorf("target %q: %w", this.name, err)
		}
		runLog, err = openLogFile(this.tcfg.Logs.Run, os.Stdout, &closers)
		if err != nil {
			for _, c := range closers {
				c.Close()
			}
			cancel()
			return fmt.Errorf("target %q: %w", this.name, err)
		}
	}

	execSumFile := strings.TrimSuffix(configFile, filepath.Ext(configFile)) + ".sum"

	opts := execrun.Options{
		RootDir:    this.rootDir,
		LogPrefix:  fmt.Sprintf("[%s:%s]", this.name, this.tcfg.EffectiveType()),
		Stdout:     runLog,
		Stderr:     runLog,
		ExecStdout: buildLog,
		ExecStderr: buildLog,
		SumFile:    execSumFile,

		OnExecStart:    this.onExecStart,
		OnExecDone:     this.onExecDone,
		OnProcessStart: this.onProcessStart,
		OnProcessExit:  this.onProcessExit,

		BuildTrigger: this.buildTrigger,
		ExecStop:     this.execStop,
		ExecStart:    this.execStart,
	}

	go func() {
		defer func() {
			for _, c := range closers {
				c.Close()
			}
		}()

		err := execrun.Run(ctx, *ecfg, opts)
		this.handleRunComplete(ctx, err)
	}()

	return nil
}

func (this *target) startGorun() error {
	configFile := filepath.Base(this.tcfg.Config)
	configPath := filepath.Join(this.rootDir, configFile)
	gcfg, err := gorun.LoadConfig(configPath)
	if err != nil {
		this.mu.Lock()
		this.state = StateError
		this.lastExecError = err.Error()
		this.mu.Unlock()
		return fmt.Errorf("target %q: load config: %w", this.name, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	this.mu.Lock()
	this.cancel = cancel
	this.mu.Unlock()

	var closers []io.Closer
	var buildLog, runLog io.Writer = os.Stdout, os.Stdout
	if this.tcfg.Logs != nil {
		var err error
		buildLog, err = openLogFile(this.tcfg.Logs.Build, os.Stdout, &closers)
		if err != nil {
			cancel()
			return fmt.Errorf("target %q: %w", this.name, err)
		}
		runLog, err = openLogFile(this.tcfg.Logs.Run, os.Stdout, &closers)
		if err != nil {
			for _, c := range closers {
				c.Close()
			}
			cancel()
			return fmt.Errorf("target %q: %w", this.name, err)
		}
	}

	gorunSumFile := strings.TrimSuffix(configFile, filepath.Ext(configFile)) + ".sum"

	opts := gorun.Options{
		RootDir:      this.rootDir,
		LogPrefix:    fmt.Sprintf("[%s]", this.name),
		Stdout:       runLog,
		Stderr:       runLog,
		BuildStdout:  buildLog,
		BuildStderr:  buildLog,
		SumFile:      gorunSumFile,

		OnBuildStart:   this.onExecStart,
		OnBuildDone:    this.onExecDone,
		OnProcessStart: this.onProcessStart,
		OnProcessExit:  this.onProcessExit,

		BuildTrigger: this.buildTrigger,
		ExecStop:     this.execStop,
		ExecStart:    this.execStart,
	}

	go func() {
		defer func() {
			for _, c := range closers {
				c.Close()
			}
		}()

		err := gorun.Run(ctx, *gcfg, opts)
		this.handleRunComplete(ctx, err)
	}()

	return nil
}

// openLogFile opens a log file for append. Returns the file as an io.Writer
// (or the fallback if path is empty) and appends the file to closers.
func openLogFile(path string, fallback io.Writer, closers *[]io.Closer) (io.Writer, error) {
	if path == "" {
		return fallback, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}
	*closers = append(*closers, f)
	return f, nil
}

func (this *target) handleRunComplete(ctx context.Context, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	if ctx.Err() != nil {
		// Context was cancelled — intentional stop
		if this.state != StateStopped {
			this.state = StateStopped
		}
	} else if err != nil {
		this.state = StateError
		this.lastExecError = err.Error()
	}
	this.pid = 0
}

func (this *target) onExecStart() {
	this.mu.Lock()
	defer this.mu.Unlock()
	now := time.Now()
	this.lastExecTime = &now
	this.state = StateStarting
	this.buildCount++
}

func (this *target) onExecDone(duration time.Duration, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	dur := duration.Seconds()
	this.lastExecDuration = &dur
	if err != nil {
		this.lastExecResult = "failed"
		this.lastExecError = err.Error()
		this.state = StateError
	} else {
		this.lastExecResult = "success"
		this.lastExecError = ""
	}
}

func (this *target) onProcessStart(pid int) {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.pid = pid
	now := time.Now()
	this.lastStartTime = &now
	this.state = StateRunning
	if this.restartCount > 0 || this.lastExecTime != nil {
		this.restartCount++
	}
}

func (this *target) onProcessExit(exitCode int, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.pid = 0
	if exitCode == 0 {
		this.state = StateExited
	} else {
		this.state = StateError
	}
}

// Build sends a build trigger (rebuild + restart).
func (this *target) Build() {
	select {
	case this.buildTrigger <- struct{}{}:
	default:
	}
}

// StartExec sends an exec start signal (start process without rebuilding).
func (this *target) StartExec() {
	select {
	case this.execStart <- struct{}{}:
	default:
	}
}

// StopExec sends an exec stop signal (stop process, keep watcher running).
func (this *target) StopExec() {
	select {
	case this.execStop <- struct{}{}:
		this.mu.Lock()
		this.state = StateStopped
		this.pid = 0
		this.mu.Unlock()
	default:
	}
}

// Stop cancels the target's run loop and lets the runner shut down gracefully
// (SIGTERM → 5s timeout → SIGKILL).
func (this *target) Stop() {
	this.mu.Lock()
	cancel := this.cancel
	this.cancel = nil
	this.state = StateStopped
	this.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// Kill cancels the target's run loop and immediately kills the process group.
func (this *target) Kill() {
	this.mu.Lock()
	cancel := this.cancel
	this.cancel = nil
	this.state = StateStopped
	pid := this.pid
	this.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if pid > 0 {
		if pgid, err := syscall.Getpgid(pid); err == nil {
			syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
}

// Status returns the current status snapshot.
func (this *target) Status() TargetStatus {
	this.mu.Lock()
	defer this.mu.Unlock()

	// Populate ResolvedURL for each link
	links := make([]Link, len(this.tcfg.Links))
	copy(links, this.tcfg.Links)
	for i := range links {
		if links[i].File != "" {
			links[i].ResolvedURL = "/api/file?path=" + url.QueryEscape(links[i].File)
		} else {
			links[i].ResolvedURL = links[i].URL
		}
	}

	return TargetStatus{
		Name:             this.name,
		Type:             this.tcfg.EffectiveType(),
		HasBuild:         this.hasBuild,
		HasRun:           this.hasRun,
		State:            this.state,
		Enabled:          this.enabled,
		PID:              this.pid,
		LastExecTime:     this.lastExecTime,
		LastExecDuration: this.lastExecDuration,
		LastExecResult:   this.lastExecResult,
		LastExecError:    this.lastExecError,
		LastStartTime:    this.lastStartTime,
		RestartCount:     this.restartCount,
		BuildCount:       this.buildCount,
		Links:            links,
		Logs:             this.tcfg.Logs,
	}
}
