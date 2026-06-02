package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/gur-shatz/go-run/pkg/supervisordeploy"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: supervisor-deploy prepare [flags]")
	}
	switch os.Args[1] {
	case "prepare":
		return runPrepare(os.Args[2:])
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func runPrepare(args []string) error {
	fs := flag.NewFlagSet("prepare", flag.ContinueOnError)
	target := fs.String("target", "", "path to target yaml")
	config := fs.String("config", "", "path to supervisor.yml")
	packages := fs.String("packages", "dist", "directory containing component tarballs")
	version := fs.String("version", "", "component version to stage (default: infer from packages)")
	out := fs.String("out", "", "output directory for deployment bundle")
	if err := fs.Parse(args); err != nil {
		return err
	}
	bundle, err := supervisordeploy.Prepare(supervisordeploy.PrepareOptions{
		TargetPath:  *target,
		ConfigPath:  *config,
		PackagesDir: *packages,
		Version:     *version,
		OutputDir:   *out,
	})
	if err != nil {
		return err
	}
	fmt.Println(bundle)
	return nil
}
