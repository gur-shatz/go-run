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

	"github.com/gur-shatz/go-run/internal/cli"
	"github.com/gur-shatz/go-run/internal/color"
	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/internal/sumfile"
	"github.com/gur-shatz/go-run/pkg/gorun"
)

func main() {
	color.Init()
	if err := run(); err != nil {
		log.Error("%v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := cli.Parse(os.Args[1:])
	if err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	log.Init(cfg.Verbose)

	// Resolve config file path
	configFile := cfg.ConfigFile
	if configFile == "" {
		configFile = cli.ConfigFileName("")
	}

	switch cfg.Command {
	case cli.CommandInit:
		return runInit(configFile)
	case cli.CommandSum:
		return runSum(configFile)
	default:
		return runWatch(cfg, configFile)
	}
}

func runInit(configFile string) error {
	if _, err := os.Stat(configFile); err == nil {
		return fmt.Errorf("%s already exists (remove it first to regenerate)", configFile)
	}

	if err := os.WriteFile(configFile, []byte(gorun.DefaultConfigYAML), 0644); err != nil {
		return fmt.Errorf("write %s: %w", configFile, err)
	}

	log.Success("Created %s", configFile)
	return nil
}

func runSum(configFile string) error {
	rootDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// Try to load watch patterns from config
	var watchPatterns []string
	configPath := filepath.Join(rootDir, configFile)
	if gcfg, err := gorun.LoadConfig(configPath); err == nil {
		watchPatterns = gcfg.Watch
		fmt.Printf("Using config: %s\n", configFile)
	}

	if len(watchPatterns) == 0 {
		watchPatterns = gorun.DefaultWatchPatterns
		fmt.Println("Using built-in defaults (**/*.go, go.mod, go.sum)")
	}

	patterns := gorun.ParseWatchPatterns(watchPatterns)
	sums, err := gorun.ScanFiles(rootDir, patterns)
	if err != nil {
		return fmt.Errorf("scan files: %w", err)
	}

	// Derive sum filename from config filename (gorun.yaml → gorun.sum)
	sumFile := strings.TrimSuffix(configFile, filepath.Ext(configFile)) + ".sum"
	if err := sumfile.Write(sumFile, sums); err != nil {
		return fmt.Errorf("write %s: %w", sumFile, err)
	}

	log.Success("Created %s (%d files)", sumFile, len(sums))
	return nil
}

func runWatch(cfg cli.Config, configFile string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println()
		cancel()
	}()

	var gcfg gorun.Config

	if cfg.BuildTarget != "" {
		// CLI provided a build target — reconstruct args string
		var parts []string
		parts = append(parts, cfg.BuildFlags...)
		parts = append(parts, cfg.BuildTarget)
		parts = append(parts, cfg.AppArgs...)
		gcfg.Args = strings.Join(parts, " ")
	} else {
		// No build target on CLI — load config file
		loaded, err := gorun.LoadConfig(configFile)
		if err != nil {
			return fmt.Errorf("no build target specified and %s not found: %w", configFile, err)
		}
		gcfg = *loaded
	}

	sumFile := strings.TrimSuffix(configFile, filepath.Ext(configFile)) + ".sum"

	opts := gorun.Options{
		PollInterval: cfg.PollInterval,
		Debounce:     cfg.Debounce,
		Verbose:      cfg.Verbose,
		LogPrefix:    "[gorun]",
		SumFile:      sumFile,
	}

	if cfg.Combined != "" {
		f, err := openOutputFile(cfg.Combined)
		if err != nil {
			return fmt.Errorf("open combined log file: %w", err)
		}
		defer f.Close()
		opts.Stdout = f
		opts.Stderr = f
		log.Verbose("Child stdout+stderr → %s", cfg.Combined)
	} else {
		if cfg.Stdout != "" {
			f, err := openOutputFile(cfg.Stdout)
			if err != nil {
				return fmt.Errorf("open stdout file: %w", err)
			}
			defer f.Close()
			opts.Stdout = f
			log.Verbose("Child stdout → %s", cfg.Stdout)
		}

		if cfg.Stderr != "" {
			f, err := openOutputFile(cfg.Stderr)
			if err != nil {
				return fmt.Errorf("open stderr file: %w", err)
			}
			defer f.Close()
			opts.Stderr = f
			log.Verbose("Child stderr → %s", cfg.Stderr)
		}
	}

	err := gorun.Run(ctx, gcfg, opts)
	if err != nil && ctx.Err() != nil {
		// Context cancelled (signal) — not an error
		return nil
	}
	return err
}

func openOutputFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
}
