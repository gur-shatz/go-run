package supervisordeploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestPackageNameRE locks in that a version containing underscores (the default
// 260622_224159_master stamp) is captured whole, rather than being eaten by a
// greedy component group. Component names must not contain '_'.
func TestPackageNameRE(t *testing.T) {
	cases := []struct {
		name              string
		comp, ver, platfm string
	}{
		{"backend_260622_224159_master_linux_arm64.tar.gz", "backend", "260622_224159_master", "linux_arm64"},
		{"backend_260622-master_linux_arm64.tar.gz", "backend", "260622-master", "linux_arm64"},
		{"backend_260609-analytics_linux_arm64.tar.gz", "backend", "260609-analytics", "linux_arm64"},
		{"frontend_260606_001610_master_linux_arm64.tar.gz", "frontend", "260606_001610_master", "linux_arm64"},
		{"gateway_260605_135232_master_darwin_amd64.tar.gz", "gateway", "260605_135232_master", "darwin_amd64"},
	}
	for _, c := range cases {
		m := packageNameRE.FindStringSubmatch(c.name)
		if m == nil {
			t.Errorf("%s: no match", c.name)
			continue
		}
		if m[1] != c.comp || m[2] != c.ver || m[3] != c.platfm {
			t.Errorf("%s: got comp=%q ver=%q plat=%q; want comp=%q ver=%q plat=%q",
				c.name, m[1], m[2], m[3], c.comp, c.ver, c.platfm)
		}
	}
}

func TestWriteValuesUsesDefaultResources(t *testing.T) {
	dir := t.TempDir()
	target := Target{Target: "dev", Namespace: "safeapi"}
	target.Supervisor.Name = "supervisor"

	if err := writeValues(dir, target); err != nil {
		t.Fatalf("writeValues: %v", err)
	}

	values := readValuesForTest(t, dir)
	resources := values["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	requests := resources["requests"].(map[string]any)

	if got, want := limits["memory"], "512Mi"; got != want {
		t.Fatalf("limits.memory = %v, want %v", got, want)
	}
	if got, want := requests["memory"], "64Mi"; got != want {
		t.Fatalf("requests.memory = %v, want %v", got, want)
	}
}

func TestWriteValuesUsesSupervisorName(t *testing.T) {
	dir := t.TempDir()
	target := Target{Target: "edge", Namespace: "safeapi-edge"}
	target.Supervisor.Name = "gwrouter"

	if err := writeValues(dir, target); err != nil {
		t.Fatalf("writeValues: %v", err)
	}

	values := readValuesForTest(t, dir)
	if got, want := values["app"], "gwrouter"; got != want {
		t.Fatalf("app = %v, want %v", got, want)
	}
	if got, want := values["configDir"], "gwrouter"; got != want {
		t.Fatalf("configDir = %v, want %v", got, want)
	}
	if got, want := values["namespace"], "safeapi-edge"; got != want {
		t.Fatalf("namespace = %v, want %v", got, want)
	}
}

func TestReadTargetDefaultsSupervisorName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.yml")
	if err := os.WriteFile(path, []byte("target: dev\nplatform: linux/arm64\nnamespace: safeapi\n"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	target, err := readTarget(path)
	if err != nil {
		t.Fatalf("readTarget: %v", err)
	}
	if got, want := target.Supervisor.Name, "supervisor"; got != want {
		t.Fatalf("supervisor.name = %v, want %v", got, want)
	}
}

func TestReadTargetRejectsInvalidSupervisorName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.yml")
	if err := os.WriteFile(path, []byte("target: dev\nplatform: linux/arm64\nnamespace: safeapi\nsupervisor:\n  name: gateway_router\n"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	_, err := readTarget(path)
	if err == nil || !strings.Contains(err.Error(), "supervisor.name invalid") {
		t.Fatalf("readTarget error = %v, want supervisor.name invalid", err)
	}
}

func TestWriteValuesUsesTargetResources(t *testing.T) {
	dir := t.TempDir()
	target := Target{Target: "prod", Namespace: "safeapi"}
	target.Supervisor.Name = "supervisor"
	target.Supervisor.Resources = Resources{
		Requests: map[string]string{"cpu": "100m", "memory": "256Mi"},
		Limits:   map[string]string{"cpu": "1", "memory": "2Gi"},
	}

	if err := writeValues(dir, target); err != nil {
		t.Fatalf("writeValues: %v", err)
	}

	values := readValuesForTest(t, dir)
	resources := values["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	requests := resources["requests"].(map[string]any)

	if got, want := limits["memory"], "2Gi"; got != want {
		t.Fatalf("limits.memory = %v, want %v", got, want)
	}
	if got, want := requests["memory"], "256Mi"; got != want {
		t.Fatalf("requests.memory = %v, want %v", got, want)
	}
}

func TestWriteValuesMergesPartialTargetResourcesWithDefaults(t *testing.T) {
	dir := t.TempDir()
	target := Target{Target: "prod", Namespace: "safeapi"}
	target.Supervisor.Name = "supervisor"
	target.Supervisor.Resources = Resources{
		Limits: map[string]string{"memory": "2Gi"},
	}

	if err := writeValues(dir, target); err != nil {
		t.Fatalf("writeValues: %v", err)
	}

	values := readValuesForTest(t, dir)
	resources := values["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	requests := resources["requests"].(map[string]any)

	if got, want := limits["memory"], "2Gi"; got != want {
		t.Fatalf("limits.memory = %v, want %v", got, want)
	}
	if got, want := limits["cpu"], "500m"; got != want {
		t.Fatalf("limits.cpu = %v, want %v", got, want)
	}
	if got, want := requests["memory"], "64Mi"; got != want {
		t.Fatalf("requests.memory = %v, want %v", got, want)
	}
}

func readValuesForTest(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatalf("read values.yaml: %v", err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(data, &values); err != nil {
		t.Fatalf("parse values.yaml: %v", err)
	}
	return values
}
