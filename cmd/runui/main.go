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
	fs := flag.NewFlagSet("runui", flag.ContinueOnError)

	configPath := fs.String("config", "runctl.yaml", "path to config file")
	fs.StringVar(configPath, "c", "runctl.yaml", "path to config file (shorthand)")
	verbose := fs.Bool("v", false, "verbose output")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: runui [flags]\n\n")
		fmt.Fprintf(os.Stderr, "All-in-one runctl controller with web dashboard.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	log.SetPrefix("[runui]")
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

	// Create chi router and mount API + UI routes
	r := chi.NewRouter()
	r.Mount("/api", ctrl.Routes())
	r.Mount("/", runui.Routes())

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.API.Port),
		Handler: r,
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stdout, "[runui] Dashboard: http://localhost:%d/\n", cfg.API.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Println("[runui] Server error:", err)
			errCh <- err
		}
		close(errCh)

	}()

	select {
	case <-ctx.Done():
		server.Close()
		return nil
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	}
}
