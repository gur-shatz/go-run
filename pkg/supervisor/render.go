package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gur-shatz/go-run/pkg/config"
)

// noValuePlaceholder is what Go's text/template emits when a key is missing
// from a map[string]any context with the "missingkey=zero" option (which
// pkg/config sets). pkg/config's higher-level Process pass catches it; the
// lower-level ResolveExpr we call from here does not, so the supervisor
// catches it explicitly to keep a rendered file from leaking "<no value>"
// to the child.
const noValuePlaceholder = "<no value>"

// tmplExtension is the suffix that marks a file inside a version dir as a
// template to be rendered at launch time. The rendered output lands beside
// it with the suffix stripped.
const tmplExtension = ".tmpl"

// renderedBackupSuffix is what gets appended to the previous rendered output
// when we re-render. Single-level backup: a fresh render replaces any
// existing .bak.
const renderedBackupSuffix = ".bak"

// defaultsFilename is the vendor-shipped baseline values file at the root
// of a version dir. Optional — missing file means the only context is the
// supervisor's vars + env + launch vars.
const defaultsFilename = "defaults.yml"

// renderVersionTemplates walks versionDir alphabetically and, for every
// "<x>.tmpl" file, renders the content to "<x>" using a merged template
// context (defaults.yml + supervisorVars + launchVars + env). If "<x>"
// already exists from a previous render it is moved to "<x>.bak"
// (replacing any prior .bak) before the new content is written.
//
// File mode of the rendered output mirrors the .tmpl file's mode so an
// executable template produces an executable rendered file.
//
// First error short-circuits; the caller treats it as an exec failure.
func renderVersionTemplates(versionDir string, supervisorVars map[string]any, launchVars LaunchVars) error {
	ctx, env, err := buildRenderContext(versionDir, supervisorVars, launchVars)
	if err != nil {
		return err
	}

	return filepath.WalkDir(versionDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), tmplExtension) {
			return nil
		}
		return renderOneTemplate(path, ctx, env)
	})
}

// buildRenderContext merges the four context layers, with documented
// precedence (high → low: env, supervisorVars, defaults, launchVars). env
// is kept separate because config.ResolveExpr takes it as its own argument
// — `{{ env "FOO" }}` reaches into it.
func buildRenderContext(versionDir string, supervisorVars map[string]any, launchVars LaunchVars) (map[string]any, map[string]string, error) {
	ctx := make(map[string]any)

	// Lowest: launch vars (so they can never silently override an explicit
	// user setting with the same name). The five fixed names live at the
	// top level of the context.
	for k, v := range launchVars.asMap() {
		ctx[k] = v
	}

	// Next: defaults.yml.
	defaults, err := readDefaultsFile(filepath.Join(versionDir, defaultsFilename))
	if err != nil {
		return nil, nil, err
	}
	for k, v := range defaults {
		ctx[k] = v
	}

	// Highest non-env: supervisor.yml vars block.
	for k, v := range supervisorVars {
		ctx[k] = v
	}

	// env is separate — accessed only via the `env` template func.
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}
	return ctx, env, nil
}

// readDefaultsFile loads defaults.yml as a map. Missing file is not an
// error (returns empty map); other read or parse errors are.
func readDefaultsFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

// renderOneTemplate reads tmplPath, resolves its content against ctx/env
// via the shared template engine, validates the result, and writes it to
// the target path (.tmpl suffix stripped). A pre-existing target is
// rotated to .bak first.
//
// Validation runs in two layers:
//  1. Unresolved variables — any "<no value>" substring (left behind by
//     text/template when missingkey=zero meets a map[string]any) fails the
//     render. Authors who want a missing var to be a hard error can use
//     {{ required "..." .X }} for a clearer message; this is the
//     catch-all.
//  2. File-type — for targets ending in .yml/.yaml/.json the rendered
//     bytes are parsed before being written. A malformed result is caught
//     here instead of by the child.
func renderOneTemplate(tmplPath string, ctx map[string]any, env map[string]string) error {
	body, err := os.ReadFile(tmplPath)
	if err != nil {
		return fmt.Errorf("read template %s: %w", tmplPath, err)
	}

	rendered, err := config.ResolveExpr(string(body), ctx, env)
	if err != nil {
		return fmt.Errorf("render %s: %w", tmplPath, err)
	}
	if strings.Contains(rendered, noValuePlaceholder) {
		return fmt.Errorf("render %s: unresolved template variable (output contains %q)", tmplPath, noValuePlaceholder)
	}

	info, err := os.Stat(tmplPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", tmplPath, err)
	}

	target := strings.TrimSuffix(tmplPath, tmplExtension)
	if err := validateRenderedByExtension(target, []byte(rendered)); err != nil {
		return fmt.Errorf("validate %s: %w", target, err)
	}
	if err := rotateBackup(target); err != nil {
		return err
	}
	if err := atomicWrite(target, []byte(rendered), info.Mode().Perm()); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}

// validateRenderedByExtension parses the rendered bytes when the target's
// extension implies a known structured format. A parse error here means the
// template produced invalid output for the format the vendor chose; better
// to fail the launch than hand a broken file to the child.
func validateRenderedByExtension(target string, body []byte) error {
	switch strings.ToLower(filepath.Ext(target)) {
	case ".yaml", ".yml":
		var x any
		return yaml.Unmarshal(body, &x)
	case ".json":
		var x any
		return json.Unmarshal(body, &x)
	}
	return nil
}

// rotateBackup renames the existing target (if any) to <target>.bak,
// removing any prior .bak first. Missing target is a no-op.
func rotateBackup(target string) error {
	if _, err := os.Stat(target); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", target, err)
	}
	bak := target + renderedBackupSuffix
	if err := os.RemoveAll(bak); err != nil {
		return fmt.Errorf("remove old %s: %w", bak, err)
	}
	if err := os.Rename(target, bak); err != nil {
		return fmt.Errorf("rotate %s → %s: %w", target, bak, err)
	}
	return nil
}

// asMap exposes the five launch vars as a string map suitable for merging
// into the template context. Defined here (not on LaunchVars itself) to
// keep template.go focused on command-line ${VAR} expansion.
func (this LaunchVars) asMap() map[string]string {
	return map[string]string{
		"VERSION":      this.Version,
		"VERSION_DIR":  this.VersionDir,
		"STATE_DIR":    this.StateDir,
		"MONITOR_PORT": fmt.Sprintf("%d", this.MonitorPort),
		"KILL_SOCK":    this.KillSock,
	}
}
