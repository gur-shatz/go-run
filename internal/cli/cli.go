package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// Command represents what gorun should do.
type Command int

const (
	CommandRun  Command = iota // default: build, run, watch
	CommandInit                // generate gorun config file
	CommandSum                 // generate gorun sum file
)

// Config holds the parsed gorun configuration.
type Config struct {
	Command      Command
	PollInterval time.Duration
	Debounce     time.Duration
	Verbose      bool
	ConfigFile   string
	Stdout       string
	Stderr       string
	Combined     string
}

// Parse parses command-line arguments into a Config.
//
// Format:
//
//	gorun [gorun-flags]
//	gorun init [-c <file>]
//	gorun sum [-c <file>]
//
// gorun always loads its configuration from a YAML file (default: gorun.yaml).
func Parse(args []string) (Config, error) {
	cfg := Config{}

	fs := flag.NewFlagSet("gorun", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.DurationVar(&cfg.PollInterval, "poll", 500*time.Millisecond, "")
	fs.DurationVar(&cfg.Debounce, "debounce", 300*time.Millisecond, "")
	fs.BoolVar(&cfg.Verbose, "v", false, "")
	fs.BoolVar(&cfg.Verbose, "verbose", false, "")
	fs.StringVar(&cfg.ConfigFile, "c", "", "")
	fs.StringVar(&cfg.ConfigFile, "config", "", "")
	fs.StringVar(&cfg.Stdout, "stdout", "", "")
	fs.StringVar(&cfg.Stderr, "stderr", "", "")
	fs.StringVar(&cfg.Combined, "combined", "", "")

	fs.Usage = func() {
		fmt.Fprint(fs.Output(), Usage())
	}

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fmt.Print(Usage())
			return cfg, err
		}
		return cfg, err
	}

	remaining := fs.Args()

	// No positional args: run from config file
	if len(remaining) == 0 {
		cfg.Command = CommandRun
		return cfg, nil
	}

	// Check for subcommands
	switch remaining[0] {
	case "init":
		cfg.Command = CommandInit
		return cfg, nil
	case "sum":
		cfg.Command = CommandSum
		return cfg, nil
	default:
		return cfg, fmt.Errorf("unknown subcommand %q\n\n%s", remaining[0], Usage())
	}
}

// SplitBuildArgs splits args into (go build flags, build target, app args).
// The build target is the first arg that doesn't start with "-" and isn't
// a value for a preceding flag. Everything before it is go build flags,
// everything after is app args.
func SplitBuildArgs(args []string) (buildFlags []string, target string, appArgs []string) {
	skipNext := false
	for i, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			return args[:i], arg, args[i+1:]
		}
		// Flags like -tags, -ldflags consume the next arg as a value
		if isBuildFlagWithValue(arg) {
			skipNext = true
		}
	}
	// No target found — all args look like flags
	return args, "", nil
}

// isBuildFlagWithValue returns true for go build flags that consume the next argument.
func isBuildFlagWithValue(arg string) bool {
	// Flags that use -flag value (not -flag=value)
	switch arg {
	case "-tags", "-ldflags", "-gcflags", "-asmflags",
		"-toolexec", "-overlay", "-pgo",
		"-p", "-pkgdir", "-mod", "-modfile":
		return true
	}
	return false
}

// FlattenTarget converts a build target path into a flattened string suitable
// for use in filenames. E.g. "./cmd/mypkg" → "cmd_mypkg", "." → "".
func FlattenTarget(target string) string {
	// Strip leading ./
	t := strings.TrimPrefix(target, "./")
	// Strip trailing /
	t = strings.TrimRight(t, "/")
	// Strip .go extension if it's a file
	t = strings.TrimSuffix(t, filepath.Ext(t))
	// Replace path separators with underscores
	t = strings.ReplaceAll(t, "/", "_")
	// "." target means current dir, no prefix
	if t == "." || t == "" {
		return ""
	}
	return t
}

// ConfigFileName returns the config filename for a given build target.
// E.g. "./cmd/mypkg" → "gorun_cmd_mypkg.yaml", "" → "gorun.yaml"
func ConfigFileName(target string) string {
	flat := FlattenTarget(target)
	if flat == "" {
		return "gorun.yaml"
	}
	return "gorun_" + flat + ".yaml"
}

// SumFileName returns the sum filename for a given build target.
// E.g. "./cmd/mypkg" → "gorun_cmd_mypkg.sum", "" → "gorun.sum"
func SumFileName(target string) string {
	flat := FlattenTarget(target)
	if flat == "" {
		return "gorun.sum"
	}
	return "gorun_" + flat + ".sum"
}

// BinFileName returns the binary filename for a given build target.
// E.g. "./cmd/mypkg" → "gorun_cmd_mypkg.bin", "" → "gorun.bin"
func BinFileName(target string) string {
	flat := FlattenTarget(target)
	if flat == "" {
		return "gorun.bin"
	}
	return "gorun_" + flat + ".bin"
}

// Usage returns the help text for gorun.
func Usage() string {
	return `gorun - Go auto-rebuild with file watching

Usage:
  gorun                          Load gorun.yaml and run
  gorun [flags]                  Load config and run with overrides
  gorun init [-c <file>]         Generate a default config file
  gorun sum [-c <file>]          Generate a sum file from watched files

Commands:
  init        Generate a default gorun config file in the current directory
  sum         Generate a gorun sum file from current watched files

  Use -c to specify a custom config file:
    gorun init                   →  creates gorun.yaml
    gorun init -c my.yaml        →  creates my.yaml
    gorun sum                    →  reads gorun.yaml for watch patterns
    gorun -c my.yaml             →  loads my.yaml and runs

Flags:
  -c, --config <file>     Config file path (default: gorun.yaml)
  --poll <duration>       Poll interval for file changes (default: 500ms)
  --debounce <duration>   Nagle debounce window (default: 300ms)
  --stdout <file>         Redirect child process stdout to file (append mode)
  --stderr <file>         Redirect child process stderr to file (append mode)
  --combined <file>       Redirect both stdout and stderr to one file (append mode)
  -v, --verbose           Verbose output (watched patterns, file counts)
  -h, --help              Show this help

Config file (gorun.yaml):
  vars:
    app_port: "8080"
  watch:
    - "**/*.go"
    - "go.mod"
    - "go.sum"
  args: "./cmd/server -port {{ .app_port }}"
  exec:
    - "go generate ./..."

Template features:
  - vars: section for defining template variables
  - {{ .VAR }} and [[ .VAR ]] syntax (both supported)
  - Functions: default, required, env, add
  - Environment variables override vars section values
`
}
