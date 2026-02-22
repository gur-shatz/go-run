package main

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/go-chi/chi/v5"

	"github.com/gur-shatz/go-run/internal/color"
	"github.com/gur-shatz/go-run/internal/configutil"
	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/internal/sumfile"
	"github.com/gur-shatz/go-run/pkg/config"
	"github.com/gur-shatz/go-run/pkg/execrun"
	"github.com/gur-shatz/go-run/pkg/runctl"
	"github.com/gur-shatz/go-run/pkg/runui"
)

// stringSlice is a flag.Value that collects repeated -t values.
type stringSlice []string

func (this *stringSlice) String() string { return strings.Join(*this, ",") }
func (this *stringSlice) Set(val string) error {
	*this = append(*this, val)
	return nil
}

func main() {
	color.Init()
	if err := run(); err != nil {
		log.Error("%v", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("runctl", flag.ContinueOnError)

	configPath := fs.String("config", "runctl.yaml", "path to config file")
	fs.StringVar(configPath, "c", "runctl.yaml", "path to config file (shorthand)")
	verbose := fs.Bool("v", false, "verbose output")
	ui := fs.Bool("ui", false, "serve embedded web dashboard")

	var targets stringSlice
	fs.Var(&targets, "t", "target name filter (repeatable)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: runctl [flags] [command]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  init    Generate a starter runctl.yaml\n")
		fmt.Fprintf(os.Stderr, "  build   Run build steps for all (or selected) targets and exit\n")
		fmt.Fprintf(os.Stderr, "  sum     Write .sum files for all (or selected) targets and exit\n")
		fmt.Fprintf(os.Stderr, "  vars    Dump resolved variables for all (or selected) targets\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  runctl                          Run with default config (runctl.yaml)\n")
		fmt.Fprintf(os.Stderr, "  runctl -ui                      Run with web dashboard\n")
		fmt.Fprintf(os.Stderr, "  runctl -c myconfig.yaml         Run with custom config\n")
		fmt.Fprintf(os.Stderr, "  runctl -t api -t web            Watch only 'api' and 'web' targets\n")
		fmt.Fprintf(os.Stderr, "  runctl build                    Build all targets and exit\n")
		fmt.Fprintf(os.Stderr, "  runctl -t api build             Build only 'api' target\n")
		fmt.Fprintf(os.Stderr, "  runctl sum                      Write sum files for all targets\n")
		fmt.Fprintf(os.Stderr, "  runctl vars                     Show resolved variables\n")
		fmt.Fprintf(os.Stderr, "  runctl -t api vars              Show variables for 'api' target\n")
		fmt.Fprintf(os.Stderr, "  runctl init                     Generate runctl.yaml\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	// Resolve .yml/.yaml fallback
	*configPath = configutil.ResolveYAMLPath(*configPath)

	args := fs.Args()
	if len(args) > 0 {
		switch args[0] {
		case "init":
			return runInit(*configPath)
		case "build":
			return runBuild(*configPath, *verbose, targets)
		case "sum":
			return runSum(*configPath, *verbose, targets)
		case "vars":
			return runVars(*configPath, targets)
		}
	}

	if *ui {
		log.SetPrefix("[runui]")
	} else {
		log.SetPrefix("[runctl]")
	}
	log.Init(*verbose)

	cfg, err := runctl.LoadConfig(*configPath)
	if err != nil {
		return err
	}

	baseDir := filepath.Dir(*configPath)

	ctrl, err := runctl.New(*cfg, baseDir)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println()
		log.Status("Shutting down...")
		cancel()
	}()

	// Validate -t filter names before starting
	if len(targets) > 0 {
		for _, name := range targets {
			if _, ok := cfg.Targets[name]; !ok {
				return fmt.Errorf("unknown target %q", name)
			}
		}
	}

	// Start targets (filtered or all enabled)
	ctrl.StartTargetsFiltered(targets)
	defer ctrl.KillTargets()

	// Create chi router and mount API routes
	r := chi.NewRouter()
	r.Mount("/api", ctrl.Routes())
	if *ui {
		r.Mount("/", runui.Routes())
	}

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.API.Port),
		Handler: r,
	}

	errCh := make(chan error, 1)
	go func() {
		if *ui {
			fmt.Fprintf(os.Stdout, "[runui] Dashboard: http://localhost:%d/\n", cfg.API.Port)
		} else {
			fmt.Fprintf(os.Stdout, "[runctl] API server listening on :%d, no UI\n", cfg.API.Port)
		}
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		server.Close()
		return nil
	case err := <-errCh:
		return fmt.Errorf("api server: %w", err)
	}
}

// resolveTargets returns the (name, TargetConfig) pairs to operate on.
// If filterNames is empty, all enabled targets are returned.
// Returns an error if a filter name doesn't exist in the config.
func resolveTargets(cfg *runctl.Config, filterNames []string) ([]targetEntry, error) {
	if len(filterNames) > 0 {
		entries := make([]targetEntry, 0, len(filterNames))
		for _, name := range filterNames {
			tcfg, ok := cfg.Targets[name]
			if !ok {
				return nil, fmt.Errorf("unknown target %q", name)
			}
			entries = append(entries, targetEntry{Name: name, Config: tcfg})
		}
		return entries, nil
	}

	entries := make([]targetEntry, 0, len(cfg.Targets))
	for name, tcfg := range cfg.Targets {
		if tcfg.IsEnabled() {
			entries = append(entries, targetEntry{Name: name, Config: tcfg})
		}
	}
	return entries, nil
}

type targetEntry struct {
	Name   string
	Config runctl.TargetConfig
}

// loadExecrunConfig loads an execrun config for a target, merging parent vars.
func loadExecrunConfig(entry targetEntry, cfg *runctl.Config, baseDir string) (*execrun.Config, string, error) {
	dir := filepath.Dir(entry.Config.Config)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}
	configFile := filepath.Base(entry.Config.Config)
	configPath := configutil.ResolveYAMLPath(filepath.Join(dir, configFile))

	parentVars := cfg.ResolvedVars
	if len(entry.Config.Vars) > 0 {
		parentVars = make(map[string]string, len(cfg.ResolvedVars)+len(entry.Config.Vars))
		maps.Copy(parentVars, cfg.ResolvedVars)
		maps.Copy(parentVars, entry.Config.Vars)
	}

	var configOpts []config.Option
	if len(parentVars) > 0 {
		configOpts = append(configOpts, config.WithVars(parentVars))
	}

	ecfg, _, err := execrun.LoadConfig(configPath, configOpts...)
	if err != nil {
		return nil, "", fmt.Errorf("target %q: load config: %w", entry.Name, err)
	}
	return ecfg, dir, nil
}

func runBuild(configPath string, verbose bool, filterNames []string) error {
	log.SetPrefix("[runctl]")
	log.Init(verbose)

	cfg, err := runctl.LoadConfig(configPath)
	if err != nil {
		return err
	}

	baseDir := filepath.Dir(configPath)
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return fmt.Errorf("resolve base dir: %w", err)
	}

	entries, err := resolveTargets(cfg, filterNames)
	if err != nil {
		return err
	}

	ctx := context.Background()
	var failed bool

	for _, entry := range entries {
		ecfg, dir, err := loadExecrunConfig(entry, cfg, absBase)
		if err != nil {
			log.Error("%s: %v", entry.Name, err)
			failed = true
			continue
		}

		opts := execrun.Options{
			RootDir:   dir,
			LogPrefix: fmt.Sprintf("[%s]", entry.Name),
			Verbose:   verbose,
		}

		log.Status("%s: building...", entry.Name)
		if err := execrun.RunBuild(ctx, *ecfg, opts); err != nil {
			log.Error("%s: build failed: %v", entry.Name, err)
			failed = true
		} else {
			log.Success("%s: build ok", entry.Name)
		}
	}

	if failed {
		return fmt.Errorf("one or more targets failed to build")
	}
	return nil
}

func runSum(configPath string, verbose bool, filterNames []string) error {
	log.SetPrefix("[runctl]")
	log.Init(verbose)

	cfg, err := runctl.LoadConfig(configPath)
	if err != nil {
		return err
	}

	baseDir := filepath.Dir(configPath)
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return fmt.Errorf("resolve base dir: %w", err)
	}

	entries, err := resolveTargets(cfg, filterNames)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		ecfg, dir, err := loadExecrunConfig(entry, cfg, absBase)
		if err != nil {
			log.Error("%s: %v", entry.Name, err)
			continue
		}

		sums, err := execrun.ScanFiles(ecfg, dir)
		if err != nil {
			log.Error("%s: scan failed: %v", entry.Name, err)
			continue
		}

		configFile := filepath.Base(entry.Config.Config)
		sumFile := strings.TrimSuffix(configFile, filepath.Ext(configFile)) + ".sum"
		sumPath := filepath.Join(dir, sumFile)

		if err := sumfile.Write(sumPath, sums); err != nil {
			log.Error("%s: write sum: %v", entry.Name, err)
			continue
		}

		log.Success("%s: wrote %s (%d files)", entry.Name, sumPath, len(sums))
	}

	return nil
}

func runVars(configPath string, filterNames []string) error {
	cfg, err := runctl.LoadConfig(configPath)
	if err != nil {
		return err
	}

	entries, err := resolveTargets(cfg, filterNames)
	if err != nil {
		return err
	}

	// Print global vars
	fmt.Println("Global vars:")
	if len(cfg.ResolvedVars) == 0 {
		fmt.Println("  (none)")
	} else {
		keys := make([]string, 0, len(cfg.ResolvedVars))
		for k := range cfg.ResolvedVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %s=%s\n", k, cfg.ResolvedVars[k])
		}
	}

	// Print per-target vars (merged: global + target overrides)
	for _, entry := range entries {
		fmt.Printf("\nTarget %q vars:\n", entry.Name)

		merged := make(map[string]string, len(cfg.ResolvedVars)+len(entry.Config.Vars))
		maps.Copy(merged, cfg.ResolvedVars)
		maps.Copy(merged, entry.Config.Vars)

		if len(merged) == 0 {
			fmt.Println("  (none)")
			continue
		}

		keys := make([]string, 0, len(merged))
		for k := range merged {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := merged[k]
			// Mark overridden vars
			if _, isOverride := entry.Config.Vars[k]; isOverride {
				if _, isGlobal := cfg.ResolvedVars[k]; isGlobal {
					fmt.Printf("  %s=%s  (overridden)\n", k, v)
					continue
				}
				fmt.Printf("  %s=%s  (target-only)\n", k, v)
				continue
			}
			fmt.Printf("  %s=%s\n", k, v)
		}
	}

	return nil
}

func runInit(configPath string) error {
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("%s already exists (remove it first to regenerate)", configPath)
	}

	if err := os.WriteFile(configPath, []byte(runctl.DefaultConfigYAML), 0644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}

	log.Init(false)
	log.Success("Created %s", configPath)
	return nil
}
