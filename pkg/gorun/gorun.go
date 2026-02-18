package gorun

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gur-shatz/go-run/internal/cli"
	"github.com/gur-shatz/go-run/internal/color"
	"github.com/gur-shatz/go-run/internal/glob"
	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/internal/notify"
	"github.com/gur-shatz/go-run/internal/runner"
	"github.com/gur-shatz/go-run/internal/scan"
	"github.com/gur-shatz/go-run/internal/sumfile"
	"github.com/gur-shatz/go-run/internal/watcher"
)

// Config describes what to build and run.
type Config struct {
	Watch []string `yaml:"watch,omitempty"` // file patterns (empty = defaults)
	Args  string   `yaml:"args"`            // build flags + target + app args, parsed like CLI
	Exec  []string `yaml:"exec,omitempty"`  // pre-build commands
}

// ParseArgs splits the Args string into build flags, build target, and app args
// using the same logic as the CLI argument parser.
func (this *Config) ParseArgs() (buildFlags []string, buildTarget string, appArgs []string) {
	fields := strings.Fields(this.Args)
	if len(fields) == 0 {
		return nil, "", nil
	}
	return cli.SplitBuildArgs(fields)
}

// LoadConfig reads and parses a gorun YAML config file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// Expand environment variables in YAML before parsing
	data = []byte(os.ExpandEnv(string(data)))

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	if cfg.Args == "" {
		return nil, fmt.Errorf("invalid config %s: args is required", path)
	}

	return &cfg, nil
}

// DefaultConfigYAML is the commented starter YAML for gorun config files.
//
//go:embed gorun.default.yaml
var DefaultConfigYAML string

// Options controls the runtime behavior of Run.
type Options struct {
	PollInterval time.Duration
	Debounce     time.Duration
	Verbose      bool
	Stdout       io.Writer
	Stderr       io.Writer
	RootDir      string
	LogPrefix    string
	SumFile      string // sum file path (relative to RootDir), e.g. "gorun.sum"

	// BuildStdout and BuildStderr override output for build steps (exec steps + go build).
	// Defaults to Stdout/Stderr if nil.
	BuildStdout io.Writer
	BuildStderr io.Writer

	OnBuildStart   func()
	OnBuildDone    func(duration time.Duration, err error)
	OnProcessStart func(pid int)
	OnProcessExit  func(exitCode int, err error)

	// External control — all optional, used by runctl for granular control.
	BuildTrigger <-chan struct{} // triggers rebuild + restart
	ExecStop     <-chan struct{} // stops just the managed process
	ExecStart    <-chan struct{} // starts just the managed process (no rebuild)
}

// DefaultWatchPatterns are used when no watch patterns are specified in config.
var DefaultWatchPatterns = []string{"**/*.go", "go.mod", "go.sum"}

// Run executes the watch-build-restart loop. Blocks until ctx is cancelled
// or the child process exits on its own.
func Run(ctx context.Context, cfg Config, opts Options) error {
	if cfg.Args == "" {
		return fmt.Errorf("args is required")
	}

	buildFlags, buildTarget, appArgs := cfg.ParseArgs()
	if buildTarget == "" {
		return fmt.Errorf("no build target found in args: %q", cfg.Args)
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
	if opts.BuildStdout == nil {
		opts.BuildStdout = opts.Stdout
	}
	if opts.BuildStderr == nil {
		opts.BuildStderr = opts.Stderr
	}

	color.Init()
	prefix := "[gorun]"
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

	n := notify.New()

	// Resolve watch patterns
	watchPatterns := cfg.Watch
	if len(watchPatterns) == 0 {
		watchPatterns = DefaultWatchPatterns
		l.Verbose("Config: built-in defaults")
	}
	patterns := ParseWatchPatterns(watchPatterns)

	l.Verbose("Watching patterns:")
	for _, p := range patterns {
		pfx := "  "
		if p.Negated {
			pfx = "  !"
		}
		l.Verbose("%s%s", pfx, p.Raw)
	}

	// Initial scan
	initialSums, err := ScanFiles(rootDir, patterns)
	if err != nil {
		return fmt.Errorf("initial scan: %w", err)
	}
	l.Verbose("Watching %d files", len(initialSums))

	// Write sum file (persisted in working directory)
	sumFile := opts.SumFile
	if sumFile == "" {
		sumFile = "gorun.sum"
	}
	sumPath := filepath.Join(rootDir, sumFile)
	if err := sumfile.Write(sumPath, initialSums); err != nil {
		return fmt.Errorf("write sum file: %w", err)
	}

	// Build and run
	binName := cli.BinFileName(buildTarget)
	r := runner.New(ctx, rootDir, buildTarget, buildFlags, appArgs, cfg.Exec, binName, opts.Stdout, opts.Stderr, opts.BuildStdout, opts.BuildStderr, l)
	defer r.Cleanup()

	if opts.OnBuildStart != nil {
		opts.OnBuildStart()
	}
	l.Status("Building...")
	buildDuration, err := r.Build()
	if opts.OnBuildDone != nil {
		opts.OnBuildDone(buildDuration, err)
	}
	if err != nil {
		return fmt.Errorf("initial build: %w", err)
	}
	l.Success("Built in %s", scan.FormatDuration(buildDuration))

	if err := r.Start(); err != nil {
		return fmt.Errorf("initial start: %w", err)
	}
	l.Success("Started: %s (pid %d)", r.CmdLine(), r.PID())
	n.Started(r.PID(), buildDuration)
	if opts.OnProcessStart != nil {
		opts.OnProcessStart(r.PID())
	}

	// Heartbeat state
	var healthy atomic.Bool
	healthy.Store(true)

	// Set up watcher
	w := watcher.New(rootDir, patterns, opts.PollInterval, opts.Debounce, func(changes sumfile.ChangeSet) {
		l.Change(changes)
		n.Changed(changes)

		if opts.OnBuildStart != nil {
			opts.OnBuildStart()
		}
		l.Status("Rebuilding...")
		buildDuration, err := r.Restart()
		if opts.OnBuildDone != nil {
			opts.OnBuildDone(buildDuration, err)
		}
		if err != nil {
			l.Error("Build failed: %v", err)
			l.Warn("Keeping previous version running.")
			n.BuildFailed(err, changes)
			healthy.Store(false)
			return
		}
		l.Success("Restarted: %s (pid %d, built in %s)", r.CmdLine(), r.PID(), scan.FormatDuration(buildDuration))
		n.Rebuilt(r.PID(), buildDuration, changes)
		healthy.Store(true)

		if opts.OnProcessStart != nil {
			opts.OnProcessStart(r.PID())
		}

		// Update sum file
		newSums, err := ScanFiles(rootDir, patterns)
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
			n.Stopping()
			return nil
		case info := <-r.Exited():
			healthy.Store(false)
			if opts.OnProcessExit != nil {
				opts.OnProcessExit(info.ExitCode, info.Err)
			}
			l.Warn("Process exited (code %d): %s.", info.ExitCode, r.CmdLine())
		case <-opts.BuildTrigger:
			if opts.OnBuildStart != nil {
				opts.OnBuildStart()
			}
			l.Status("Build triggered...")
			buildDuration, err := r.Restart()
			if opts.OnBuildDone != nil {
				opts.OnBuildDone(buildDuration, err)
			}
			if err != nil {
				l.Error("Build failed: %v", err)
				healthy.Store(false)
			} else {
				l.Success("Restarted: %s (pid %d, built in %s)", r.CmdLine(), r.PID(), scan.FormatDuration(buildDuration))
				healthy.Store(true)
				if opts.OnProcessStart != nil {
					opts.OnProcessStart(r.PID())
				}
			}
		case <-opts.ExecStop:
			l.Status("Stopping process...")
			r.Stop()
		case <-opts.ExecStart:
			l.Status("Starting process...")
			if err := r.Start(); err != nil {
				l.Error("Start failed: %v", err)
			} else {
				l.Success("Started: %s (pid %d)", r.CmdLine(), r.PID())
				if opts.OnProcessStart != nil {
					opts.OnProcessStart(r.PID())
				}
			}
		case <-ticker.C:
			l.Tick(healthy.Load() && r.Running())
		}
	}
}

// ParseWatchPatterns converts string patterns to glob.Pattern slice.
func ParseWatchPatterns(watch []string) []glob.Pattern {
	return scan.ParseWatchPatterns(watch)
}

// ScanFiles expands watch patterns and hashes all matching files.
// Returns a map of relative path → hash.
func ScanFiles(rootDir string, patterns []glob.Pattern) (map[string]string, error) {
	return scan.ScanFiles(rootDir, patterns)
}
