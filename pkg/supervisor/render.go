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

// noValuePlaceholder is what Go's text/template emits when a key is missing.
// pkg/config normally catches it; this is a defensive validation check.
const noValuePlaceholder = "<no value>"

// tmplExtension is the suffix that marks a file inside a version dir as a
// template suitable for best-effort validation.
const tmplExtension = ".tmpl"

// renderedBackupSuffix is kept for the legacy render helper. Normal supervisor
// launch does not render app config files.
const renderedBackupSuffix = ".bak"

// manifestFilename is the vendor-shipped manifest file at the root of a
// version dir:
//
//	validate_templates: # files to best-effort validate; "config.yml" means "config.yml.tmpl"
//	- config.yml
//	default_vars:
//	  GREETING: Hello
//
// For compatibility, legacyDefaultsFilename remains valid as a flat default
// vars file until existing bundles have moved to manifest.yml.
const manifestFilename = "manifest.yml"
const legacyDefaultsFilename = "defaults.yml"

func validateVersionTemplatesWithEnv(versionDir string, supervisorVars map[string]any, componentEnv map[string]string, launchVars LaunchVars) error {
	env, err := buildRenderEnv(versionDir, supervisorVars, componentEnv, launchVars)
	if err != nil {
		return err
	}

	templates, err := validateTemplatePaths(versionDir)
	if err != nil {
		return err
	}
	for _, path := range templates {
		if err := validateOneTemplate(path, env); err != nil {
			return err
		}
	}
	return nil
}

// buildRenderEnv builds the same env-shaped data model the child process gets.
// Later layers override earlier layers. The real process environment remains
// available for secrets and undeclared values; manifest default_vars is
// vendor baseline; supervisor vars are deployment-scoped; component env is
// component-scoped; launch facts are final so VERSION, BUILD_DIR, and ports
// stay tied to the version being launched.
func buildRenderEnv(versionDir string, supervisorVars map[string]any, componentEnv map[string]string, launchVars LaunchVars) (map[string]string, error) {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}

	defaults, err := readDefaultVars(versionDir)
	if err != nil {
		return nil, err
	}
	for k, v := range defaults {
		env[k] = fmt.Sprintf("%v", v)
	}

	for k, v := range supervisorVars {
		env[k] = fmt.Sprintf("%v", v)
	}

	for k, v := range componentEnv {
		env[k] = v
	}

	for k, v := range EnvMap(launchVars) {
		env[k] = v
	}
	return env, nil
}

type versionManifest struct {
	ValidateTemplates []string       `yaml:"validate_templates"`
	Templates         []string       `yaml:"templates"` // legacy name; treated as validate_templates.
	DefaultVars       map[string]any `yaml:"default_vars"`
}

func readVersionManifestFile(path string) (versionManifest, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return versionManifest{}, false, nil
		}
		return versionManifest{}, false, fmt.Errorf("read %s: %w", path, err)
	}

	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return versionManifest{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if out == nil {
		return versionManifest{}, true, nil
	}
	_, hasValidateTemplates := out["validate_templates"]
	_, hasTemplates := out["templates"]
	_, hasDefaultVars := out["default_vars"]
	if !hasValidateTemplates && !hasTemplates && !hasDefaultVars {
		return versionManifest{DefaultVars: out}, true, nil
	}

	var manifest versionManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return versionManifest{}, true, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if manifest.DefaultVars == nil {
		manifest.DefaultVars = map[string]any{}
	}
	if len(manifest.ValidateTemplates) == 0 && len(manifest.Templates) > 0 {
		manifest.ValidateTemplates = manifest.Templates
	}
	return manifest, true, nil
}

func readVersionManifest(versionDir string) (versionManifest, bool, error) {
	manifest, ok, err := readVersionManifestFile(filepath.Join(versionDir, manifestFilename))
	if err != nil || ok {
		return manifest, ok, err
	}
	return readVersionManifestFile(filepath.Join(versionDir, legacyDefaultsFilename))
}

// readDefaultVars loads manifest default vars. Missing file is not an
// error (returns empty map); other read or parse errors are.
func readDefaultVars(versionDir string) (map[string]any, error) {
	manifest, _, err := readVersionManifest(versionDir)
	if err != nil {
		return nil, err
	}
	if manifest.DefaultVars == nil {
		return map[string]any{}, nil
	}
	return manifest.DefaultVars, nil
}

func validateTemplatePaths(versionDir string) ([]string, error) {
	manifest, _, err := readVersionManifest(versionDir)
	if err != nil {
		return nil, err
	}
	if len(manifest.ValidateTemplates) > 0 {
		paths := make([]string, 0, len(manifest.ValidateTemplates))
		for _, rel := range manifest.ValidateTemplates {
			path, err := manifestTemplatePath(versionDir, rel)
			if err != nil {
				return nil, err
			}
			paths = append(paths, path)
		}
		return paths, nil
	}

	return nil, nil
}

func manifestTemplatePath(versionDir, rel string) (string, error) {
	if rel == "" {
			return "", fmt.Errorf("%s validate_templates entry is empty", manifestFilename)
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s validate_templates entry %q must be relative", manifestFilename, rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s validate_templates entry %q escapes version dir", manifestFilename, rel)
	}
	if !strings.HasSuffix(clean, tmplExtension) {
		clean += tmplExtension
	}
	path := filepath.Join(versionDir, clean)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s validate_templates entry %q missing source %s", manifestFilename, rel, path)
		}
		return "", fmt.Errorf("stat template %s: %w", path, err)
	}
	return path, nil
}

// renderOneTemplate reads tmplPath, resolves its content against ctx/env
// via the shared config engine, validates the result, and writes it to the
// target path (.tmpl suffix stripped). A pre-existing target is
// rotated to .bak first.
//
// Validation runs in two layers:
//  1. The shared config processor catches missing vars, supports vars:
//     sections, both {{ }} and [[ ]] delimiters, and runctl's template funcs.
//  2. File-type — for targets ending in .yml/.yaml/.json the rendered
//     bytes are parsed before being written. A malformed result is caught
//     here instead of by the child.
func renderOneTemplate(tmplPath string, env map[string]string) error {
	rendered, target, mode, err := renderTemplateBytes(tmplPath, env)
	if err != nil {
		return err
	}
	if err := rotateBackup(target); err != nil {
		return err
	}
	if err := atomicWrite(target, rendered, mode); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}

func validateOneTemplate(tmplPath string, env map[string]string) error {
	_, _, _, err := renderTemplateBytes(tmplPath, env)
	return err
}

func renderTemplateBytes(tmplPath string, env map[string]string) ([]byte, string, os.FileMode, error) {
	body, err := os.ReadFile(tmplPath)
	if err != nil {
		return nil, "", 0, fmt.Errorf("read template %s: %w", tmplPath, err)
	}

	rendered, _, err := config.Process(body, config.WithEnv(env))
	if err != nil {
		return nil, "", 0, fmt.Errorf("render %s: %w", tmplPath, err)
	}
	if strings.Contains(string(rendered), noValuePlaceholder) {
		return nil, "", 0, fmt.Errorf("render %s: unresolved template variable (output contains %q)", tmplPath, noValuePlaceholder)
	}

	info, err := os.Stat(tmplPath)
	if err != nil {
		return nil, "", 0, fmt.Errorf("stat %s: %w", tmplPath, err)
	}

	target := strings.TrimSuffix(tmplPath, tmplExtension)
	if err := validateRenderedByExtension(target, rendered); err != nil {
		return nil, "", 0, fmt.Errorf("validate %s: %w", target, err)
	}
	return rendered, target, info.Mode().Perm(), nil
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
