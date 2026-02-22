package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/go-chi/chi/v5"

	"github.com/gur-shatz/go-run/internal/color"
	"github.com/gur-shatz/go-run/internal/configutil"
	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/pkg/runctl"
	"github.com/gur-shatz/go-run/pkg/runui"
)

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

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: runctl [flags] [command]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  init    Generate a starter runctl.yaml\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  runctl                          Run with default config (runctl.yaml)\n")
		fmt.Fprintf(os.Stderr, "  runctl -ui                      Run with web dashboard\n")
		fmt.Fprintf(os.Stderr, "  runctl -c myconfig.yaml         Run with custom config\n")
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

	// Start all enabled targets
	ctrl.StartTargets()
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
			fmt.Fprintf(os.Stdout, "[runctl] API server listening on :%d\n", cfg.API.Port)
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
