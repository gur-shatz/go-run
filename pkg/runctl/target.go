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

	"github.com/gur-shatz/go-run/internal/configutil"
	"github.com/gur-shatz/go-run/internal/sumfile"
	"github.com/gur-shatz/go-run/pkg/backoffice"
	boclient "github.com/gur-shatz/go-run/pkg/backoffice/client"
	"github.com/gur-shatz/go-run/pkg/config"
	"github.com/gur-shatz/go-run/pkg/execrun"
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

// PhaseStatus is the structured status for a build/test phase.
type PhaseStatus struct {
	Time     *time.Time `json:"time,omitempty"`
	Duration *float64   `json:"duration_secs,omitempty"`
	Result   string     `json:"result,omitempty"`
	Error    string     `json:"error,omitempty"`
	Count    int        `json:"count"`
}

// TargetStatus is the JSON-serializable status of a target.
type TargetStatus struct {
	Name         string      `json:"name"`
	Title        string      `json:"title,omitempty"`
	Description  string      `json:"description,omitempty"`
	HasBuild     bool        `json:"has_build"`
	HasTest      bool        `json:"has_test"`
	HasRun       bool        `json:"has_run"`
	State        TargetState `json:"state"`
	CurrentStage string      `json:"current_stage,omitempty"`
	Enabled      bool        `json:"enabled"`
	PID          int         `json:"pid,omitempty"`

	Build PhaseStatus `json:"build"`
	Test  PhaseStatus `json:"test"`

	LastBuildTime     *time.Time `json:"last_build_time,omitempty"`
	LastBuildDuration *float64   `json:"last_build_duration_secs,omitempty"`
	LastBuildResult   string     `json:"last_build_result,omitempty"`
	LastBuildError    string     `json:"last_build_error,omitempty"`

	LastTestTime     *time.Time `json:"last_test_time,omitempty"`
	LastTestDuration *float64   `json:"last_test_duration_secs,omitempty"`
	LastTestResult   string     `json:"last_test_result,omitempty"`
	LastTestError    string     `json:"last_test_error,omitempty"`

	LastExecTime     *time.Time `json:"last_exec_time,omitempty"`          // deprecated alias for build
	LastExecDuration *float64   `json:"last_exec_duration_secs,omitempty"` // deprecated alias for build
	LastExecResult   string     `json:"last_exec_result,omitempty"`        // deprecated alias for build
	LastExecError    string     `json:"last_exec_error,omitempty"`         // deprecated alias for build

	LastStartTime      *time.Time `json:"last_start_time,omitempty"`
	LastFileChangeTime *time.Time `json:"last_file_change_time,omitempty"`
	RestartCount       int        `json:"restart_count"`
	BuildCount         int        `json:"build_count"`
	TestCount          int        `json:"test_count"`

	Links []Link      `json:"links,omitempty"`
	Logs  *LogsConfig `json:"logs,omitempty"`

	BackofficeReady  bool                   `json:"backoffice_ready"`
	BackofficeStatus *backoffice.StatusInfo `json:"backoffice_status,omitempty"`
}

// target wraps a target config and manages its lifecycle.
type target struct {
	name        string
	tcfg        TargetConfig
	rootDir     string            // absolute path to target working directory
	parentVars  map[string]string // resolved vars from parent (runctl) config
	verbose     bool
	title       string
	description string
	hasBuild    bool
	hasTest     bool
	hasRun      bool

	mu           sync.Mutex
	state        TargetState
	currentStage string
	enabled      bool
	cancel       context.CancelFunc
	pid          int

	lastBuildTime      *time.Time
	lastBuildDuration  *float64
	lastBuildResult    string
	lastBuildError     string
	lastTestTime       *time.Time
	lastTestDuration   *float64
	lastTestResult     string
	lastTestError      string
	lastStartTime      *time.Time
	lastFileChangeTime *time.Time
	restartCount       int
	buildCount         int
	testCount          int

	buildTrigger chan struct{}
	testTrigger  chan struct{}
	execStop     chan struct{}
	execStart    chan struct{}

	backofficeClient *boclient.Client
	backofficeReady  bool
}

func newTarget(name string, tcfg TargetConfig, baseDir string, parentVars map[string]string, verbose bool) *target {
	dir := filepath.Dir(tcfg.Config)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}

	return &target{
		name:         name,
		tcfg:         tcfg,
		rootDir:      dir,
		parentVars:   parentVars,
		verbose:      verbose,
		hasBuild:     false,
		hasTest:      false,
		hasRun:       true,
		state:        StateIdle,
		enabled:      tcfg.IsEnabled(),
		buildTrigger: make(chan struct{}, 1),
		testTrigger:  make(chan struct{}, 1),
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

	return this.start()
}

func (this *target) start() error {
	configFile := filepath.Base(this.tcfg.Config)
	configPath := configutil.ResolveYAMLPath(filepath.Join(this.rootDir, configFile))
	var configOpts []config.Option
	if len(this.parentVars) > 0 {
		configOpts = append(configOpts, config.WithVars(this.parentVars))
	}
	ecfg, _, err := execrun.LoadConfig(configPath, configOpts...)
	if err != nil {
		this.mu.Lock()
		this.state = StateError
		this.currentStage = "build"
		this.lastBuildError = err.Error()
		this.mu.Unlock()
		return fmt.Errorf("target %q: load config: %w", this.name, err)
	}

	this.hasBuild = len(ecfg.BuildSteps()) > 0
	this.hasTest = len(ecfg.TestSteps()) > 0
	this.hasRun = !ecfg.IsBuildOnly()
	this.title = ecfg.Title
	this.description = ecfg.Description

	ctx, cancel := context.WithCancel(context.Background())
	this.mu.Lock()
	this.cancel = cancel
	this.mu.Unlock()

	var closers []io.Closer
	var buildLog, testLog, runLog io.Writer = os.Stdout, os.Stdout, os.Stdout
	if this.tcfg.Logs != nil {
		var err error
		buildLog, err = openLogFile(this.tcfg.Logs.Build, os.Stdout, &closers)
		if err != nil {
			cancel()
			return fmt.Errorf("target %q: %w", this.name, err)
		}
		testLog, err = openLogFile(this.tcfg.Logs.Test, os.Stdout, &closers)
		if err != nil {
			for _, c := range closers {
				c.Close()
			}
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
		RootDir:   this.rootDir,
		LogPrefix: fmt.Sprintf("[%s]", this.name),
		Verbose:   this.verbose,
		Stdout:    runLog,
		Stderr:    runLog,
		SumFile:   execSumFile,

		ExecStdout: buildLog,
		ExecStderr: buildLog,
		TestStdout: testLog,
		TestStderr: testLog,

		OnBuildStart:      this.onBuildStart,
		OnBuildDone:       this.onBuildDone,
		OnTestStart:       this.onTestStart,
		OnTestDone:        this.onTestDone,
		OnFilesChanged:    this.onFilesChanged,
		OnProcessStart:    this.onProcessStart,
		OnProcessExit:     this.onProcessExit,
		OnBackofficeReady: this.onBackofficeReady,

		BuildTrigger: this.buildTrigger,
		TestTrigger:  this.testTrigger,
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

func phaseSnapshot(t *time.Time, d *float64, result, err string, count int) PhaseStatus {
	return PhaseStatus{
		Time:     t,
		Duration: d,
		Result:   result,
		Error:    err,
		Count:    count,
	}
}

type phaseFields struct {
	time     **time.Time
	duration **float64
	result   *string
	err      *string
	count    *int
}

func (this *target) phaseFields(stage string) *phaseFields {
	switch stage {
	case "build":
		return &phaseFields{
			time:     &this.lastBuildTime,
			duration: &this.lastBuildDuration,
			result:   &this.lastBuildResult,
			err:      &this.lastBuildError,
			count:    &this.buildCount,
		}
	case "test":
		return &phaseFields{
			time:     &this.lastTestTime,
			duration: &this.lastTestDuration,
			result:   &this.lastTestResult,
			err:      &this.lastTestError,
			count:    &this.testCount,
		}
	default:
		return nil
	}
}

func (this *target) markPhaseStart(stage string, at time.Time) {
	p := this.phaseFields(stage)
	if p == nil {
		return
	}
	*p.time = &at
	this.currentStage = stage
	this.state = StateStarting
}

func (this *target) markPhaseDone(stage string, duration time.Duration, err error, countEnabled bool) {
	p := this.phaseFields(stage)
	if p == nil {
		return
	}
	dur := duration.Seconds()
	*p.duration = &dur
	if err != nil {
		*p.result = "failed"
		*p.err = err.Error()
		this.state = StateError
		return
	}
	*p.result = "success"
	*p.err = ""
	if countEnabled {
		*p.count++
	}
}

func (this *target) inferFailedStage() string {
	switch {
	case this.lastTestResult == "failed" || this.lastTestError != "":
		return "test"
	case this.lastBuildResult == "failed" || this.lastBuildError != "":
		return "build"
	default:
		return ""
	}
}

func (this *target) markPhaseErrored(stage string, err error) {
	if stage == "" {
		stage = this.inferFailedStage()
	}
	if stage == "" {
		stage = "build"
	}

	p := this.phaseFields(stage)
	if p != nil {
		if *p.result == "" {
			*p.result = "failed"
		}
		if *p.err == "" {
			*p.err = err.Error()
		}
	}
	this.currentStage = stage
	this.state = StateError
}

func (this *target) clearRuntimeState() {
	this.pid = 0
	this.backofficeClient = nil
	this.backofficeReady = false
}

func (this *target) markRunStart(pid int, at time.Time) {
	hadStartedBefore := this.lastStartTime != nil
	this.pid = pid
	this.lastStartTime = &at
	this.currentStage = "run"
	this.state = StateRunning
	if this.restartCount > 0 || hadStartedBefore {
		this.restartCount++
	}
}

func (this *target) markRunExit(exitCode int) {
	this.pid = 0
	this.currentStage = ""
	this.backofficeClient = nil
	this.backofficeReady = false
	if exitCode == 0 {
		this.state = StateExited
	} else {
		this.state = StateError
	}
}

func (this *target) handleRunComplete(ctx context.Context, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	if ctx.Err() != nil {
		// Context was cancelled — intentional stop
		if this.state != StateStopped {
			this.state = StateStopped
		}
		this.currentStage = ""
	} else if err != nil {
		this.markPhaseErrored(this.currentStage, err)
	}
	this.clearRuntimeState()
}

func (this *target) onBuildStart() {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.markPhaseStart("build", time.Now())
}

func (this *target) onBuildDone(duration time.Duration, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.markPhaseDone("build", duration, err, this.hasBuild)
}

func (this *target) onTestStart() {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.markPhaseStart("test", time.Now())
}

func (this *target) onTestDone(duration time.Duration, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.markPhaseDone("test", duration, err, this.hasTest)
}

func (this *target) onFilesChanged(at time.Time, _ sumfile.ChangeSet) {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.lastFileChangeTime = &at
}

func (this *target) onProcessStart(pid int) {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.markRunStart(pid, time.Now())
}

func (this *target) onProcessExit(exitCode int, err error) {
	this.mu.Lock()
	defer this.mu.Unlock()
	_ = err
	this.markRunExit(exitCode)
}

func (this *target) onBackofficeReady(sockPath string) {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.backofficeClient = boclient.New(sockPath)
	this.backofficeReady = true
}

// BackofficeClient returns the backoffice client if the child's backoffice is ready.
func (this *target) BackofficeClient() *boclient.Client {
	this.mu.Lock()
	defer this.mu.Unlock()
	return this.backofficeClient
}

// Build sends a build trigger (rebuild + restart).
func (this *target) Build() {
	select {
	case this.buildTrigger <- struct{}{}:
	default:
	}
}

// Test sends a test trigger (tests only).
func (this *target) Test() {
	select {
	case this.testTrigger <- struct{}{}:
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

	ts := TargetStatus{
		Name:               this.name,
		Title:              this.title,
		Description:        this.description,
		HasBuild:           this.hasBuild,
		HasTest:            this.hasTest,
		HasRun:             this.hasRun,
		State:              this.state,
		CurrentStage:       this.currentStage,
		Enabled:            this.enabled,
		PID:                this.pid,
		Build:              phaseSnapshot(this.lastBuildTime, this.lastBuildDuration, this.lastBuildResult, this.lastBuildError, this.buildCount),
		Test:               phaseSnapshot(this.lastTestTime, this.lastTestDuration, this.lastTestResult, this.lastTestError, this.testCount),
		LastBuildTime:      this.lastBuildTime,
		LastBuildDuration:  this.lastBuildDuration,
		LastBuildResult:    this.lastBuildResult,
		LastBuildError:     this.lastBuildError,
		LastTestTime:       this.lastTestTime,
		LastTestDuration:   this.lastTestDuration,
		LastTestResult:     this.lastTestResult,
		LastTestError:      this.lastTestError,
		LastExecTime:       this.lastBuildTime,
		LastExecDuration:   this.lastBuildDuration,
		LastExecResult:     this.lastBuildResult,
		LastExecError:      this.lastBuildError,
		LastStartTime:      this.lastStartTime,
		LastFileChangeTime: this.lastFileChangeTime,
		RestartCount:       this.restartCount,
		BuildCount:         this.buildCount,
		TestCount:          this.testCount,
		Links:              links,
		Logs:               this.tcfg.Logs,
		BackofficeReady:    this.backofficeReady,
	}

	// Best-effort fetch of backoffice status
	if this.backofficeClient != nil {
		boClient := this.backofficeClient
		this.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if info, err := boClient.Status(ctx); err == nil {
			ts.BackofficeStatus = info
		}
		this.mu.Lock()
	}

	return ts
}
