package execrun

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gur-shatz/go-run/internal/color"
	"github.com/gur-shatz/go-run/internal/glob"
	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/internal/scan"
	"github.com/gur-shatz/go-run/internal/sumfile"
	"github.com/gur-shatz/go-run/internal/watcher"
	"github.com/gur-shatz/go-run/pkg/config"
)

// Config represents the execrun.yaml configuration.
// Build commands are preparation steps that run to completion.
// Exec commands run the managed process — the last exec command is the
// long-running process whose lifecycle is managed (SIGTERM/SIGKILL on restart).
// If build is non-empty and exec is empty, the target is build-only.
type Config struct {
	Watch []string `yaml:"watch"`
	Build []string `yaml:"build,omitempty"` // prep commands, run to completion
	Exec  []string `yaml:"exec,omitempty"`  // run commands; last is the managed process
}

// IsBuildOnly returns true when there are no exec commands (build-only target).
func (this *Config) IsBuildOnly() bool {
	return len(this.Exec) == 0
}

// Options controls the runtime behavior of Run.
type Options struct {
	PollInterval time.Duration
	Debounce     time.Duration
	Verbose      bool
	Stdout       io.Writer
	Stderr       io.Writer

	// RootDir overrides the working directory (default: os.Getwd()).
	// Commands are executed with this as the working directory.
	RootDir string

	// LogPrefix overrides the log prefix (default: "[execrun]").
	LogPrefix string

	SumFile string // sum file path (relative to RootDir), e.g. "execrun.sum"

	// ExecStdout and ExecStderr override output for exec steps (build commands).
	// Defaults to Stdout/Stderr if nil.
	ExecStdout io.Writer
	ExecStderr io.Writer

	// Lifecycle callbacks — all optional.
	OnExecStart    func()                                 // called before exec steps run
	OnExecDone     func(duration time.Duration, err error) // called after exec steps complete
	OnProcessStart func(pid int)                           // called when the run command starts
	OnProcessExit  func(exitCode int, err error)           // called when the run command exits

	// External control — all optional, used by runctl for granular control.
	BuildTrigger <-chan struct{} // triggers rebuild + restart
	ExecStop     <-chan struct{} // stops just the managed process
	ExecStart    <-chan struct{} // starts just the managed process (no rebuild)
}

// exitInfo describes how the child process exited.
type exitInfo struct {
	ExitCode int
	Err      error
}

// LoadConfig reads and parses a YAML config file.
// Accepts optional config.Option values to control template processing
// (e.g. config.WithVars to inject parent variables from runctl).
func LoadConfig(path string, opts ...config.Option) (*Config, map[string]string, error) {
	data, vars, err := config.ProcessFile(path, opts...)
	if err != nil {
		return nil, nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, vars, nil
}

// DefaultConfig returns a sensible starter config.
func DefaultConfig() Config {
	return Config{
		Watch: []string{"**/*.go", "go.mod", "go.sum"},
		Build: []string{"go build -o ./bin/app ."},
		Exec:  []string{"./bin/app"},
	}
}

// WriteConfig writes a Config to a YAML file.
func WriteConfig(path string, cfg Config) error {
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// Validate checks that the config has required fields and trims whitespace
// from commands (YAML literal blocks add trailing newlines).
func (this *Config) Validate() error {
	if len(this.Watch) == 0 {
		return fmt.Errorf("watch must have at least one pattern")
	}
	if len(this.Build)+len(this.Exec) == 0 {
		return fmt.Errorf("at least one build or exec command is required")
	}
	for i := range this.Build {
		this.Build[i] = strings.TrimSpace(this.Build[i])
	}
	for i := range this.Exec {
		this.Exec[i] = strings.TrimSpace(this.Exec[i])
	}
	return nil
}

// Steps returns the build (preparation) commands.
func (this *Config) Steps() []string {
	return this.Build
}

// RunCmd returns the last exec command (the long-running managed process).
// Returns "" if there are no exec commands (build-only).
func (this *Config) RunCmd() string {
	if len(this.Exec) == 0 {
		return ""
	}
	return this.Exec[len(this.Exec)-1]
}

// runner manages the lifecycle of the child process.
type runner struct {
	cfg     Config
	opts    Options
	ctx     context.Context
	stdout  io.Writer
	stderr  io.Writer
	rootDir string
	log     *log.Logger

	mu       sync.Mutex
	cmd      *exec.Cmd
	exited   chan exitInfo
	stopping bool
}

func newRunner(ctx context.Context, cfg Config, opts Options, rootDir string, logger *log.Logger) *runner {
	return &runner{
		cfg:     cfg,
		opts:    opts,
		ctx:     ctx,
		stdout:  opts.Stdout,
		stderr:  opts.Stderr,
		rootDir: rootDir,
		log:     logger,
		exited:  make(chan exitInfo, 1),
	}
}

// execSteps runs all exec commands except the last (preparation steps).
// Commands are cancelled if the runner's context is done.
// Returns the total duration and any error.
func (this *runner) execSteps() (time.Duration, error) {
	if this.opts.OnExecStart != nil {
		this.opts.OnExecStart()
	}

	start := time.Now()

	for _, cmd := range this.cfg.Steps() {
		this.log.Verbose("Running: %s", cmd)
		c := exec.CommandContext(this.ctx, "sh", "-c", cmd)
		c.Dir = this.rootDir
		c.Stdout = this.opts.ExecStdout
		c.Stderr = this.opts.ExecStderr
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		c.Cancel = func() error {
			return killProcessGroup(c.Process, syscall.SIGTERM)
		}
		c.WaitDelay = 5 * time.Second
		if err := c.Run(); err != nil {
			dur := time.Since(start)
			if this.opts.OnExecDone != nil {
				this.opts.OnExecDone(dur, err)
			}
			return dur, fmt.Errorf("command %q failed: %w", cmd, err)
		}
	}

	dur := time.Since(start)
	if this.opts.OnExecDone != nil {
		this.opts.OnExecDone(dur, nil)
	}
	return dur, nil
}

// start runs the run command.
func (this *runner) start() error {
	this.mu.Lock()
	defer this.mu.Unlock()

	this.stopping = false
	this.cmd = exec.Command("sh", "-c", this.cfg.RunCmd())
	this.cmd.Dir = this.rootDir
	this.cmd.Stdout = this.stdout
	this.cmd.Stderr = this.stderr
	this.cmd.Stdin = os.Stdin
	this.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := this.cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	if this.opts.OnProcessStart != nil {
		this.opts.OnProcessStart(this.cmd.Process.Pid)
	}

	cmd := this.cmd
	go func() {
		err := cmd.Wait()

		this.mu.Lock()
		wasStopping := this.stopping
		if this.cmd == cmd {
			this.cmd = nil
		}
		this.mu.Unlock()

		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}

		if !wasStopping {
			if this.opts.OnProcessExit != nil {
				this.opts.OnProcessExit(exitCode, err)
			}
			select {
			case this.exited <- exitInfo{ExitCode: exitCode, Err: err}:
			default:
			}
		}
	}()

	return nil
}

// stop kills the running process group. SIGTERM → 5s → SIGKILL.
func (this *runner) stop() error {
	this.mu.Lock()
	cmd := this.cmd
	this.cmd = nil
	this.stopping = true
	this.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	// Kill the entire process group (shell + children)
	if err := killProcessGroup(cmd.Process, syscall.SIGTERM); err != nil {
		return nil
	}

	done := make(chan struct{})
	go func() {
		cmd.Process.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		this.log.Warn("Process group didn't exit after SIGTERM, sending SIGKILL...")
		killProcessGroup(cmd.Process, syscall.SIGKILL)
		<-done
		return nil
	}
}

// kill immediately sends SIGKILL to the process group without waiting.
func (this *runner) kill() {
	this.mu.Lock()
	cmd := this.cmd
	this.cmd = nil
	this.stopping = true
	this.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	killProcessGroup(cmd.Process, syscall.SIGKILL)
}

// killProcessGroup sends a signal to the entire process group.
func killProcessGroup(p *os.Process, sig syscall.Signal) error {
	pgid, err := syscall.Getpgid(p.Pid)
	if err != nil {
		// Fallback: signal just the process
		return p.Signal(sig)
	}
	return syscall.Kill(-pgid, sig)
}

// restart runs preparation steps, stops old process, starts new one.
// If any step fails, the old process keeps running.
func (this *runner) restart() (time.Duration, error) {
	buildDuration, err := this.execSteps()
	if err != nil {
		return buildDuration, err
	}

	if err := this.stop(); err != nil {
		return buildDuration, fmt.Errorf("stop: %w", err)
	}

	// Drain stale exit info
	select {
	case <-this.exited:
	default:
	}

	if err := this.start(); err != nil {
		return buildDuration, fmt.Errorf("start: %w", err)
	}

	return buildDuration, nil
}

// running returns whether the child process is alive.
func (this *runner) running() bool {
	this.mu.Lock()
	defer this.mu.Unlock()
	return this.cmd != nil && this.cmd.Process != nil
}

// pid returns the PID of the running process, or 0.
func (this *runner) pid() int {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.cmd != nil && this.cmd.Process != nil {
		return this.cmd.Process.Pid
	}
	return 0
}

// cleanup stops the process.
func (this *runner) cleanup() {
	this.stop()
}

// Run executes the full watch-exec loop. Blocks until ctx is cancelled
// or the child process exits on its own.
func Run(ctx context.Context, cfg Config, opts Options) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	if opts.PollInterval == 0 {
		opts.PollInterval = 500 * time.Millisecond
	}
	if opts.Debounce == 0 {
		opts.Debounce = 300 * time.Millisecond
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.ExecStdout == nil {
		opts.ExecStdout = opts.Stdout
	}
	if opts.ExecStderr == nil {
		opts.ExecStderr = opts.Stderr
	}

	color.Init()
	prefix := "[execrun]"
	if opts.LogPrefix != "" {
		prefix = opts.LogPrefix
	}
	l := log.New(prefix, opts.Verbose)

	rootDir := opts.RootDir
	if rootDir == "" {
		var err error
		rootDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
	}

	// Convert watch patterns
	patterns := scan.ParseWatchPatterns(cfg.Watch)

	l.Verbose("Watching patterns:")
	for _, p := range patterns {
		pfx := "  "
		if p.Negated {
			pfx = "  !"
		}
		l.Verbose("%s%s", pfx, p.Raw)
	}

	// Initial scan
	initialSums, err := scan.ScanFiles(rootDir, patterns)
	if err != nil {
		return fmt.Errorf("initial scan: %w", err)
	}
	l.Verbose("Watching %d files", len(initialSums))

	// Write sum file (persisted in working directory)
	sumFile := opts.SumFile
	if sumFile == "" {
		sumFile = "execrun.sum"
	}
	sumPath := filepath.Join(rootDir, sumFile)
	if err := sumfile.Write(sumPath, initialSums); err != nil {
		return fmt.Errorf("write sum file: %w", err)
	}

	// Execute steps and start process
	r := newRunner(ctx, cfg, opts, rootDir, l)
	defer r.cleanup()

	if cfg.IsBuildOnly() {
		return runBuildOnly(ctx, r, rootDir, patterns, initialSums, sumPath, opts, l)
	}

	if len(cfg.Steps()) > 0 {
		l.Status("Executing...")
		dur, err := r.execSteps()
		if err != nil {
			return fmt.Errorf("exec failed: %w", err)
		}
		l.Success("Done in %s", scan.FormatDuration(dur))
	}

	if err := r.start(); err != nil {
		return fmt.Errorf("initial start: %w", err)
	}
	l.Success("Started (pid %d).", r.pid())

	// Heartbeat state
	var healthy atomic.Bool
	healthy.Store(true)

	// Set up watcher
	w := watcher.New(rootDir, patterns, opts.PollInterval, opts.Debounce, func(changes sumfile.ChangeSet) {
		l.Change(changes)

		l.Status("Launching Execs...")
		dur, err := r.restart()
		if err != nil {
			l.Error("Exec failed: %v", err)
			l.Warn("Keeping previous process running.")
			healthy.Store(false)
			return
		}
		l.Success("Launching Execs Done (pid %d, %s).", r.pid(), scan.FormatDuration(dur))
		healthy.Store(true)

		// Update sum file
		newSums, err := scan.ScanFiles(rootDir, patterns)
		if err == nil {
			if writeErr := sumfile.Write(sumPath, newSums); writeErr != nil {
				l.Verbose("update sum file: %v", writeErr)
			}
		}
	}, l)
	w.SetCurrentSums(initialSums)

	go w.Run(ctx)

	// Heartbeat ticker
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.Status("Shutting down...")
			return nil
		case info := <-r.exited:
			if info.ExitCode != 0 {
				healthy.Store(false)
				l.Error("Exited with code %d. Waiting for file changes...", info.ExitCode)
			} else {
				l.Status("Completed. Waiting for file changes...")
			}
		case <-opts.BuildTrigger:
			l.Status("Build triggered...")
			dur, err := r.restart()
			if err != nil {
				l.Error("Build failed: %v", err)
				healthy.Store(false)
			} else {
				l.Success("Build done (pid %d, %s).", r.pid(), scan.FormatDuration(dur))
				healthy.Store(true)
			}
		case <-opts.ExecStop:
			l.Status("Stopping process...")
			r.stop()
		case <-opts.ExecStart:
			l.Status("Starting process...")
			if err := r.start(); err != nil {
				l.Error("Start failed: %v", err)
			} else {
				l.Success("Started (pid %d).", r.pid())
			}
		case <-ticker.C:
			l.Tick(healthy.Load())
		}
	}
}

// runBuildOnly handles build mode: run all commands as steps, then watch for
// changes and re-run. No managed process is started.
func runBuildOnly(ctx context.Context, r *runner, rootDir string, patterns []glob.Pattern, initialSums map[string]string, sumPath string, opts Options, l *log.Logger) error {
	l.Status("Build mode: executing all commands...")
	dur, err := r.execSteps()
	if err != nil {
		return fmt.Errorf("build failed: %w", err)
	}
	l.Success("Build done in %s. Watching for changes...", scan.FormatDuration(dur))

	var healthy atomic.Bool
	healthy.Store(true)

	w := watcher.New(rootDir, patterns, r.opts.PollInterval, r.opts.Debounce, func(changes sumfile.ChangeSet) {
		l.Change(changes)

		l.Status("Rebuilding...")
		dur, err := r.execSteps()
		if err != nil {
			l.Error("Build failed: %v", err)
			healthy.Store(false)
			return
		}
		l.Success("Build done in %s", scan.FormatDuration(dur))
		healthy.Store(true)

		newSums, err := scan.ScanFiles(rootDir, patterns)
		if err == nil {
			if writeErr := sumfile.Write(sumPath, newSums); writeErr != nil {
				l.Verbose("update sum file: %v", writeErr)
			}
		}
	}, l)
	w.SetCurrentSums(initialSums)

	go w.Run(ctx)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.Status("Shutting down...")
			return nil
		case <-opts.BuildTrigger:
			l.Status("Build triggered...")
			dur, err := r.execSteps()
			if err != nil {
				l.Error("Build failed: %v", err)
				healthy.Store(false)
			} else {
				l.Success("Build done in %s", scan.FormatDuration(dur))
				healthy.Store(true)
			}
		case <-ticker.C:
			l.Tick(healthy.Load())
		}
	}
}

// ScanFiles expands watch patterns from a Config and hashes all matching files.
// Returns a map of relative path → hash. Used by the sum command.
// If rootDir is provided, it is used instead of the current working directory.
func ScanFiles(cfg *Config, rootDir ...string) (map[string]string, error) {
	dir := ""
	if len(rootDir) > 0 && rootDir[0] != "" {
		dir = rootDir[0]
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
	}
	patterns := scan.ParseWatchPatterns(cfg.Watch)
	return scan.ScanFiles(dir, patterns)
}

// DefaultConfigYAML is the commented starter YAML for `execrun init`.
//
//go:embed execrun.default.yaml
var DefaultConfigYAML string
