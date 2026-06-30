package supervisordeploy

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gur-shatz/go-run/pkg/config"
	"github.com/gur-shatz/go-run/pkg/supervisor"
)

type PrepareOptions struct {
	TargetPath  string
	ConfigPath  string
	PackagesDir string
	Version     string
	OutputDir   string
}

type Target struct {
	Target     string `yaml:"target"`
	Platform   string `yaml:"platform"`
	SSHHost    string `yaml:"ssh_host"`
	Kubeconfig string `yaml:"kubeconfig"`
	Namespace  string `yaml:"namespace"`
	Domains    struct {
		Public     string `yaml:"public"`
		Backoffice string `yaml:"backoffice"`
	} `yaml:"domains"`
	Access struct {
		AllowedCIDRs      []string `yaml:"allowed_cidrs"`
		TrustedProxyCIDRs []string `yaml:"trusted_proxy_cidrs"`
	} `yaml:"access"`
	Global struct {
		HostPath  string `yaml:"host_path"`
		MountPath string `yaml:"mount_path"`
	} `yaml:"global"`
	Supervisor struct {
		ImageRepo      string  `yaml:"image_repo"`
		ImageTag       string  `yaml:"image_tag"`
		BackofficePort int     `yaml:"backoffice_port"`
		PublicPort     int     `yaml:"public_port"`
		Routes         []Route `yaml:"routes"`
	} `yaml:"supervisor"`
	// Env is committed, per-target pod environment, projected into
	// .Values.env at bundle time. It is the lowest-priority env layer: the
	// host-side values.local.yaml is layered after the generated values.yaml,
	// so any key it sets wins over the same key here. Intended for non-secret
	// deployment settings (public URLs, modes, ports); keep secrets in the
	// host values.local.yaml.
	Env map[string]string `yaml:"env"`
}

type Route struct {
	Name string `yaml:"name" json:"name"`
	Host string `yaml:"host" json:"host"`
	Port int    `yaml:"port" json:"port"`
	// TLS selects origin-cert behavior for this route's Ingress:
	//   "" / "acme"  -> cert-manager letsencrypt-prod (default, public ACME cert)
	//   "selfsigned" -> cert-manager selfsigned-issuer; no ACME challenge, so it
	//                   works for wildcard hosts. Cloudflare Full (not strict)
	//                   accepts the self-signed origin cert.
	//   "none"       -> no tls block; plaintext origin behind Cloudflare.
	TLS string `yaml:"tls,omitempty" json:"tls,omitempty"`
}

type bundleManifest struct {
	Target     string   `json:"target"`
	Version    string   `json:"version"`
	Platform   string   `json:"platform"`
	Namespace  string   `json:"namespace"`
	CreatedAt  string   `json:"created_at"`
	Components []string `json:"components"`
}

// packageNameRE parses <component>_<version>_<goos>_<goarch>.tar.gz.
//
// The component group must not contain '_' so that a version carrying
// underscores (e.g. the default 260622_224159_master stamp) is captured whole
// by the greedy middle group instead of being eaten by a greedy first group.
// Component names therefore may use letters, digits, and '-', but not '_'.
var packageNameRE = regexp.MustCompile(`^([A-Za-z0-9-]+)_(.+)_([A-Za-z0-9]+_[A-Za-z0-9]+)\.tar\.gz$`)

func Prepare(opts PrepareOptions) (string, error) {
	if opts.TargetPath == "" {
		return "", fmt.Errorf("target path is required")
	}
	if opts.ConfigPath == "" {
		return "", fmt.Errorf("config path is required")
	}
	if opts.PackagesDir == "" {
		return "", fmt.Errorf("packages dir is required")
	}

	target, err := readTarget(opts.TargetPath)
	if err != nil {
		return "", err
	}
	if opts.Version == "" {
		opts.Version, err = inferVersion(opts.PackagesDir)
		if err != nil {
			return "", err
		}
	}
	if opts.OutputDir == "" {
		opts.OutputDir = filepath.Join(opts.PackagesDir, "deploy")
	}

	goos, goarch, err := parsePlatform(target.Platform)
	if err != nil {
		return "", err
	}
	platformName := goos + "_" + goarch
	bundleName := fmt.Sprintf("supervisor-%s-%s-%s", target.Target, opts.Version, platformName)
	stageRoot := filepath.Join(opts.OutputDir, ".stage", bundleName)
	if err := os.RemoveAll(filepath.Dir(stageRoot)); err != nil {
		return "", fmt.Errorf("clean stage: %w", err)
	}
	if err := os.MkdirAll(stageRoot, 0755); err != nil {
		return "", fmt.Errorf("create stage: %w", err)
	}

	if err := copyEmbeddedChart(filepath.Join(stageRoot, "chart")); err != nil {
		return "", err
	}
	// Load leniently: we only need the component names here. supervisor.yml is
	// copied verbatim (below) and its {{ env ... | required }} vars are resolved
	// in-pod at boot, where the real environment exists. Enforcing `required`
	// at bundle time would fail on values that are intentionally absent now.
	cfg, err := supervisor.LoadConfig(opts.ConfigPath, config.WithLenient())
	if err != nil {
		return "", fmt.Errorf("load supervisor config: %w", err)
	}
	if err := copyFile(opts.ConfigPath, filepath.Join(stageRoot, "global", "supervisor", "supervisor.yml"), 0644); err != nil {
		return "", err
	}

	components, err := stageOrigin(opts.PackagesDir, stageRoot, opts.Version, platformName, componentNames(cfg.Components))
	if err != nil {
		return "", err
	}
	if err := writeValues(stageRoot, target); err != nil {
		return "", err
	}
	if err := writeDeployScript(stageRoot, target); err != nil {
		return "", err
	}
	if err := writeManifest(stageRoot, bundleManifest{
		Target:     target.Target,
		Version:    opts.Version,
		Platform:   target.Platform,
		Namespace:  target.Namespace,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Components: components,
	}); err != nil {
		return "", err
	}

	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}
	out := filepath.Join(opts.OutputDir, bundleName+".bundle.tar.gz")
	if err := tarGz(filepath.Dir(stageRoot), bundleName, out); err != nil {
		return "", err
	}
	_ = os.RemoveAll(filepath.Dir(stageRoot))
	return out, nil
}

func readTarget(path string) (Target, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Target{}, fmt.Errorf("read target %s: %w", path, err)
	}
	var target Target
	if err := yaml.Unmarshal(data, &target); err != nil {
		return Target{}, fmt.Errorf("parse target %s: %w", path, err)
	}
	if target.Target == "" {
		return Target{}, fmt.Errorf("target.target is required")
	}
	if target.Platform == "" {
		return Target{}, fmt.Errorf("target.platform is required")
	}
	if target.Namespace == "" {
		target.Namespace = target.Target
	}
	if target.Global.MountPath == "" {
		target.Global.MountPath = "/global"
	}
	if target.Supervisor.ImageRepo == "" {
		target.Supervisor.ImageRepo = "localhost/supervisor"
	}
	if target.Supervisor.ImageTag == "" {
		target.Supervisor.ImageTag = "latest"
	}
	if target.Supervisor.BackofficePort == 0 {
		target.Supervisor.BackofficePort = 9090
	}
	if target.Supervisor.PublicPort == 0 {
		target.Supervisor.PublicPort = 8081
	}
	if len(target.Supervisor.Routes) == 0 && target.Domains.Public != "" {
		target.Supervisor.Routes = []Route{{
			Name: "public",
			Host: target.Domains.Public,
			Port: target.Supervisor.PublicPort,
		}}
	}
	for i, route := range target.Supervisor.Routes {
		if route.Name == "" {
			return Target{}, fmt.Errorf("supervisor.routes[%d].name is required", i)
		}
		if route.Host == "" {
			return Target{}, fmt.Errorf("supervisor.routes[%d].host is required", i)
		}
		if route.Port == 0 {
			return Target{}, fmt.Errorf("supervisor.routes[%d].port is required", i)
		}
		switch route.TLS {
		case "", "acme", "selfsigned", "none":
		default:
			return Target{}, fmt.Errorf("supervisor.routes[%d].tls invalid: %q (want acme, selfsigned, or none)", i, route.TLS)
		}
	}
	return target, nil
}

func parsePlatform(platform string) (string, string, error) {
	parts := strings.Split(platform, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("platform must be GOOS/GOARCH, got %q", platform)
	}
	return parts[0], parts[1], nil
}

func inferVersion(packagesDir string) (string, error) {
	entries, err := os.ReadDir(packagesDir)
	if err != nil {
		return "", fmt.Errorf("read packages dir %s: %w", packagesDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := packageNameRE.FindStringSubmatch(entry.Name())
		if len(match) == 4 {
			return match[2], nil
		}
	}
	return "", fmt.Errorf("could not infer version from %s", packagesDir)
}

func stageOrigin(packagesDir, stageRoot, version, platformName string, required []string) ([]string, error) {
	entries, err := os.ReadDir(packagesDir)
	if err != nil {
		return nil, fmt.Errorf("read packages dir %s: %w", packagesDir, err)
	}
	requiredSet := make(map[string]bool, len(required))
	for _, component := range required {
		requiredSet[component] = true
	}
	found := make(map[string]bool, len(required))
	var components []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := packageNameRE.FindStringSubmatch(entry.Name())
		if len(match) != 4 || match[2] != version || match[3] != platformName {
			continue
		}
		component := match[1]
		if len(requiredSet) > 0 && !requiredSet[component] {
			continue
		}
		components = append(components, component)
		found[component] = true
		dstDir := filepath.Join(stageRoot, "global", "origin", component)
		if err := os.MkdirAll(filepath.Join(dstDir, "images"), 0755); err != nil {
			return nil, fmt.Errorf("create origin images for %s: %w", component, err)
		}
		if err := os.MkdirAll(filepath.Join(dstDir, "versions"), 0755); err != nil {
			return nil, fmt.Errorf("create origin versions for %s: %w", component, err)
		}
		dstName := fmt.Sprintf("%s_%s.tar.gz", version, platformName)
		if err := copyFile(filepath.Join(packagesDir, entry.Name()), filepath.Join(dstDir, "images", dstName), 0644); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dstDir, "versions", "required.txt"), []byte(version+"\n"), 0644); err != nil {
			return nil, fmt.Errorf("write required.txt for %s: %w", component, err)
		}
	}
	if len(components) == 0 {
		return nil, fmt.Errorf("no packages found for version %s platform %s in %s", version, platformName, packagesDir)
	}
	for _, component := range required {
		if !found[component] {
			return nil, fmt.Errorf("missing package for component %s version %s platform %s in %s", component, version, platformName, packagesDir)
		}
	}
	return components, nil
}

func componentNames(components []supervisor.ComponentConfig) []string {
	out := make([]string, 0, len(components))
	for _, component := range components {
		out = append(out, component.Name)
	}
	return out
}

func copyEmbeddedChart(dst string) error {
	return fs.WalkDir(Assets, "chart", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("chart", path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0755)
		}
		data, err := Assets.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
			return err
		}
		return os.WriteFile(out, data, 0644)
	})
}

func writeValues(stageRoot string, target Target) error {
	env := make(map[string]any, len(target.Env))
	for k, v := range target.Env {
		env[k] = v
	}
	values := map[string]any{
		"app":       "supervisor",
		"namespace": target.Namespace,
		"image": map[string]any{
			"repository": target.Supervisor.ImageRepo,
			"tag":        target.Supervisor.ImageTag,
		},
		"global": map[string]any{
			"hostPath":  target.Global.HostPath,
			"mountPath": target.Global.MountPath,
		},
		"ports": map[string]any{
			"backoffice": target.Supervisor.BackofficePort,
			"public":     target.Supervisor.PublicPort,
		},
		"routes": target.Supervisor.Routes,
		"domains": map[string]any{
			"backoffice": target.Domains.Backoffice,
			"public":     target.Domains.Public,
		},
		"access": map[string]any{
			"allowedCIDRs":      target.Access.AllowedCIDRs,
			"trustedProxyCIDRs": target.Access.TrustedProxyCIDRs,
		},
		"env": env,
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "25m", "memory": "64Mi"},
			"limits":   map[string]any{"cpu": "500m", "memory": "512Mi"},
		},
	}
	data, err := yaml.Marshal(values)
	if err != nil {
		return fmt.Errorf("marshal values: %w", err)
	}
	return os.WriteFile(filepath.Join(stageRoot, "values.yaml"), data, 0644)
}

func writeDeployScript(stageRoot string, target Target) error {
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
HOST="${HOST:-%s}"
KUBECONFIG_PATH="${KUBECONFIG:-%s}"
NAMESPACE=%s
BUNDLE_NAME="$(basename "$ROOT")"
LOCAL_VALUES=%s

if ! command -v helm >/dev/null 2>&1; then
  echo "helm not installed or not on PATH" >&2
  exit 1
fi

echo "==> seed global storage on $HOST"
tar -C "$ROOT" -czf "$ROOT/global.tar.gz" global
scp "$ROOT/global.tar.gz" "$HOST:/tmp/$BUNDLE_NAME.global.tar.gz"
ssh "$HOST" "sudo mkdir -p %s && sudo tar -C %s -xzf /tmp/$BUNDLE_NAME.global.tar.gz --strip-components=1 && sudo chown -R 65532:65532 %s && rm -f /tmp/$BUNDLE_NAME.global.tar.gz"
rm -f "$ROOT/global.tar.gz"

echo "==> helm upgrade --install supervisor"
HELM_VALUES=(-f "$ROOT/values.yaml")
if ssh "$HOST" "test -f '$LOCAL_VALUES'"; then
  scp "$HOST:$LOCAL_VALUES" "$ROOT/values.local.yaml"
  HELM_VALUES+=(-f "$ROOT/values.local.yaml")
else
  echo "warning: $LOCAL_VALUES not found; deploying without target-local values" >&2
fi

helm --kubeconfig "$KUBECONFIG_PATH" upgrade --install supervisor "$ROOT/chart" \
  -n "$NAMESPACE" \
  --create-namespace \
  "${HELM_VALUES[@]}" \
  --wait \
  --timeout 2m
`, target.SSHHost, target.Kubeconfig, shellQuote(target.Namespace), shellQuote(target.Global.HostPath+"/supervisor/values.local.yaml"), shellQuote(target.Global.HostPath), shellQuote(target.Global.HostPath), shellQuote(target.Global.HostPath))
	return os.WriteFile(filepath.Join(stageRoot, "deploy.sh"), []byte(script), 0755)
}

func writeManifest(stageRoot string, manifest bundleManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(stageRoot, "manifest.json"), data, 0644)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create dir for %s: %w", dst, err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}

func tarGz(root, name, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create bundle %s: %w", dst, err)
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	base := filepath.Join(root, name)
	return filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
