package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gur-shatz/go-run/internal/log"
)

// Runner manages building and running a Go binary.
type Runner struct {
	ctx          context.Context
	rootDir      string // working directory for commands
	buildTarget  string
	buildFlags   []string
	appArgs      []string
	execSteps    []string
	binName      string
	stdout       io.Writer
	stderr       io.Writer
	buildStdout  io.Writer
	buildStderr  io.Writer
	log          *log.Logger

	mu       sync.Mutex
	cmd      *exec.Cmd
	binPath  string
	exited   chan ExitInfo
	stopping bool // true when Stop() initiated the kill
}

// ExitInfo describes how the child process exited.
type ExitInfo struct {
	ExitCode int
	Err      error
}

// New creates a new Runner. rootDir is the working directory for all commands.
// buildStdout/buildStderr are used for exec steps and go build output; if nil
// they default to stdout/stderr.
func New(ctx context.Context, rootDir string, buildTarget string, buildFlags []string, appArgs []string, execSteps []string, binName string, stdout, stderr, buildStdout, buildStderr io.Writer, logger *log.Logger) *Runner {
	if buildStdout == nil {
		buildStdout = stdout
	}
	if buildStderr == nil {
		buildStderr = stderr
	}
	return &Runner{
		ctx:          ctx,
		rootDir:      rootDir,
		buildTarget:  buildTarget,
		buildFlags:   buildFlags,
		appArgs:      appArgs,
		execSteps:    execSteps,
		binName:      binName,
		stdout:       stdout,
		stderr:       stderr,
		buildStdout:  buildStdout,
		buildStderr:  buildStderr,
		log:          logger,
		exited:       make(chan ExitInfo, 1),
	}
}

// BinPath returns the path to the current temp binary.
func (this *Runner) BinPath() string {
	this.mu.Lock()
	defer this.mu.Unlock()
	return this.binPath
}

// Exited returns a channel that receives when the child process exits
// on its own (not killed by Stop/Restart). Check this in your main loop
// to detect when the application has finished.
func (this *Runner) Exited() <-chan ExitInfo {
	return this.exited
}

// Build compiles the build target to a temp binary. If execSteps are set,
// runs each step before building. Returns the build duration and any error.
// Does not stop the running process on failure.
func (this *Runner) Build() (time.Duration, error) {
	start := time.Now()

	// Run pre-build exec steps
	for _, step := range this.execSteps {
		this.log.Verbose("Running: %s", step)
		cmd := exec.CommandContext(this.ctx, "sh", "-c", step)
		cmd.Dir = this.rootDir
		cmd.Stdout = this.buildStdout
		cmd.Stderr = this.buildStderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			return killProcessGroup(cmd.Process, syscall.SIGTERM)
		}
		cmd.WaitDelay = 5 * time.Second
		if err := cmd.Run(); err != nil {
			return time.Since(start), fmt.Errorf("exec step %q failed: %w", step, err)
		}
	}

	binPath := filepath.Join(os.TempDir(), this.binName)

	buildArgs := []string{"build", "-o", binPath}
	buildArgs = append(buildArgs, this.buildFlags...)
	buildArgs = append(buildArgs, this.buildTarget)
	cmd := exec.CommandContext(this.ctx, "go", buildArgs...)
	cmd.Dir = this.rootDir
	cmd.Stdout = this.buildStdout
	cmd.Stderr = this.buildStderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return killProcessGroup(cmd.Process, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		os.Remove(binPath)
		return time.Since(start), fmt.Errorf("build failed: %w", err)
	}
	elapsed := time.Since(start)

	this.mu.Lock()
	this.binPath = binPath
	this.mu.Unlock()

	return elapsed, nil
}

// Start runs the compiled binary. Must call Build first.
func (this *Runner) Start() error {
	this.mu.Lock()
	defer this.mu.Unlock()

	if this.binPath == "" {
		return fmt.Errorf("no binary built yet")
	}

	this.stopping = false
	this.cmd = exec.Command(this.binPath, this.appArgs...)
	this.cmd.Dir = this.rootDir
	this.cmd.Stdout = this.stdout
	this.cmd.Stderr = this.stderr
	this.cmd.Stdin = os.Stdin
	this.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := this.cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Wait in background; notify via exited channel if the process
	// exits on its own (not killed by us).
	cmd := this.cmd
	go func() {
		err := cmd.Wait()

		this.mu.Lock()
		wasStopping := this.stopping
		// Clear cmd if this is still the active process
		if this.cmd == cmd {
			this.cmd = nil
		}
		this.mu.Unlock()

		if !wasStopping {
			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = 1
				}
			}
			select {
			case this.exited <- ExitInfo{ExitCode: exitCode, Err: err}:
			default:
			}
		}
	}()

	return nil
}

// CmdLine returns the full command line (binary + args) for logging.
func (this *Runner) CmdLine() string {
	parts := append([]string{this.buildTarget}, this.appArgs...)
	return strings.Join(parts, " ")
}

// Running returns whether the child process is currently alive.
func (this *Runner) Running() bool {
	this.mu.Lock()
	defer this.mu.Unlock()
	return this.cmd != nil && this.cmd.Process != nil
}

// PID returns the PID of the running process, or 0 if not running.
func (this *Runner) PID() int {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.cmd != nil && this.cmd.Process != nil {
		return this.cmd.Process.Pid
	}
	return 0
}

// Stop kills the running process group. Sends SIGTERM, then SIGKILL after 5s.
func (this *Runner) Stop() error {
	this.mu.Lock()
	cmd := this.cmd
	this.cmd = nil
	this.stopping = true
	this.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	// Kill the entire process group (binary + any children)
	if err := killProcessGroup(cmd.Process, syscall.SIGTERM); err != nil {
		// Process already exited
		return nil
	}

	// Wait up to 5s for graceful shutdown
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

// Kill immediately sends SIGKILL to the process group without waiting.
func (this *Runner) Kill() {
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

// Restart stops the old process, builds, and starts a new one.
// On build failure, the old process is NOT stopped.
func (this *Runner) Restart() (time.Duration, error) {
	buildDuration, err := this.Build()
	if err != nil {
		return buildDuration, err
	}

	if err := this.Stop(); err != nil {
		return buildDuration, fmt.Errorf("stop: %w", err)
	}

	// Drain any stale exit info. The old process may have exited on its own
	// during the build (e.g. go generate modified files it was watching),
	// which would leave an ExitInfo in the channel that the main loop would
	// misinterpret as the new process exiting.
	select {
	case <-this.exited:
	default:
	}

	if err := this.Start(); err != nil {
		return buildDuration, fmt.Errorf("start: %w", err)
	}

	return buildDuration, nil
}

// Cleanup stops the process and removes the temp binary.
func (this *Runner) Cleanup() {
	this.Stop()
	this.mu.Lock()
	if this.binPath != "" {
		os.Remove(this.binPath)
		this.binPath = ""
	}
	this.mu.Unlock()
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
