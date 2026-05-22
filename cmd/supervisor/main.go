package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gur-shatz/go-run/internal/buildinfo"
	"github.com/gur-shatz/go-run/internal/color"
	"github.com/gur-shatz/go-run/internal/log"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

func main() {
	color.Init()
	log.SetPrefix("[supervisor]")
	if err := run(); err != nil {
		log.Error("%v", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("supervisor", flag.ContinueOnError)

	configPath := fs.String("config", "/etc/go-run/config.yml", "path to supervisor config file")
	fs.StringVar(configPath, "c", "/etc/go-run/config.yml", "path to supervisor config file (shorthand)")
	verbose := fs.Bool("v", false, "verbose output")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "supervisor %s\n\n", buildinfo.String())
		fmt.Fprintf(os.Stderr, "Usage: supervisor [flags] [command]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  (default)  Run the supervisor in the foreground\n")
		fmt.Fprintf(os.Stderr, "  install    Install supervisor as a system service (not yet implemented)\n")
		fmt.Fprintf(os.Stderr, "  uninstall  Remove the installed system service (not yet implemented)\n")
		fmt.Fprintf(os.Stderr, "  version    Print version and exit\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	log.Init(*verbose)

	args := fs.Args()
	if len(args) > 0 {
		switch args[0] {
		case "version":
			fmt.Println(buildinfo.String())
			return nil
		case "install", "uninstall":
			return fmt.Errorf("%s: not yet implemented", args[0])
		default:
			return fmt.Errorf("unknown command %q", args[0])
		}
	}

	cfg, err := supervisor.LoadConfig(*configPath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.StateDir, 0755); err != nil {
		return fmt.Errorf("create state_dir %s: %w", cfg.StateDir, err)
	}

	lock, err := supervisor.AcquireLock(supervisor.NewPaths(cfg.StateDir).SupervisorLock())
	if err != nil {
		return err
	}
	defer lock.Release()

	sup, err := supervisor.New(*cfg, supervisor.Options{Verbose: *verbose})
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

	return sup.Run(ctx)
}
