package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gur-shatz/go-run/internal/color"
	"github.com/gur-shatz/go-run/internal/configutil"
	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/internal/sumfile"
	"github.com/gur-shatz/go-run/pkg/execrun"
)

func main() {
	color.Init()
	if err := run(); err != nil {
		log.Error("%v", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("execrun", flag.ContinueOnError)

	configPath := fs.String("config", "execrun.yaml", "path to config file")
	fs.StringVar(configPath, "c", "execrun.yaml", "path to config file (shorthand)")
	poll := fs.Duration("poll", 500*time.Millisecond, "poll interval")
	debounce := fs.Duration("debounce", 300*time.Millisecond, "debounce duration")
	verbose := fs.Bool("v", false, "verbose output")
	stdoutFile := fs.String("stdout", "", "redirect child stdout to file")
	stderrFile := fs.String("stderr", "", "redirect child stderr to file")
	combinedFile := fs.String("combined", "", "redirect both stdout and stderr to one file")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: execrun [flags] [command]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  init    Generate a starter config file\n")
		fmt.Fprintf(os.Stderr, "  sum     Snapshot watched file hashes to execrun.sum\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  execrun                          Run with default config (execrun.yaml)\n")
		fmt.Fprintf(os.Stderr, "  execrun -c myapp.yaml            Run with custom config\n")
		fmt.Fprintf(os.Stderr, "  execrun init                     Generate execrun.yaml\n")
		fmt.Fprintf(os.Stderr, "  execrun -c myapp.yaml init       Generate myapp.yaml\n")
		fmt.Fprintf(os.Stderr, "  execrun sum                      Snapshot file hashes\n")
		fmt.Fprintf(os.Stderr, "  execrun -c myapp.yaml sum        Snapshot using custom config\n\n")
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

	// Check for subcommands
	args := fs.Args()
	if len(args) > 0 {
		switch args[0] {
		case "init":
			return runInit(*configPath)
		case "sum":
			return runSum(*configPath)
		}
	}

	log.Init(*verbose)

	// Load config
	cfg, err := execrun.LoadConfig(*configPath)
	if err != nil {
		return err
	}

	log.Verbose("Config: %s", *configPath)

	// Use the config file's directory as the root directory so that watch
	// patterns are always resolved relative to the config, regardless of
	// where the binary is invoked from.
	configAbs, err := filepath.Abs(*configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	rootDir := filepath.Dir(configAbs)
	sumFile := strings.TrimSuffix(filepath.Base(*configPath), filepath.Ext(*configPath)) + ".sum"

	// Set up stdout/stderr writers
	opts := execrun.Options{
		PollInterval: *poll,
		Debounce:     *debounce,
		Verbose:      *verbose,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		SumFile:      sumFile,
		RootDir:      rootDir,
	}

	if *combinedFile != "" {
		f, err := os.OpenFile(*combinedFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("open combined log file: %w", err)
		}
		defer f.Close()
		opts.Stdout = f
		opts.Stderr = f
		log.Verbose("Child stdout+stderr → %s", *combinedFile)
	} else {
		if *stdoutFile != "" {
			f, err := os.OpenFile(*stdoutFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
			if err != nil {
				return fmt.Errorf("open stdout file: %w", err)
			}
			defer f.Close()
			opts.Stdout = f
			log.Verbose("Child stdout → %s", *stdoutFile)
		}

		if *stderrFile != "" {
			f, err := os.OpenFile(*stderrFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
			if err != nil {
				return fmt.Errorf("open stderr file: %w", err)
			}
			defer f.Close()
			opts.Stderr = f
			log.Verbose("Child stderr → %s", *stderrFile)
		}
	}

	// Signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println()
		cancel()
	}()

	err = execrun.Run(ctx, *cfg, opts)
	if err != nil && ctx.Err() != nil {
		// Context cancelled (signal) — not an error
		return nil
	}
	return err
}

func runSum(configPath string) error {
	log.Init(false)

	cfg, err := execrun.LoadConfig(configPath)
	if err != nil {
		return err
	}

	configAbs, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	rootDir := filepath.Dir(configAbs)

	fmt.Printf("Using config: %s\n", configPath)

	sums, err := execrun.ScanFiles(cfg, rootDir)
	if err != nil {
		return fmt.Errorf("scan files: %w", err)
	}

	sumFile := filepath.Join(rootDir, strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))+".sum")
	if err := sumfile.Write(sumFile, sums); err != nil {
		return fmt.Errorf("write %s: %w", sumFile, err)
	}

	log.Success("Created %s (%d files)", sumFile, len(sums))
	return nil
}

func runInit(configPath string) error {
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("%s already exists (remove it first to regenerate)", configPath)
	}

	if err := os.WriteFile(configPath, []byte(execrun.DefaultConfigYAML), 0644); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}

	log.Init(false)
	log.Success("Created %s", configPath)
	return nil
}
