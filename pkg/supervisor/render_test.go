package supervisor

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("validateVersionTemplates", func() {
	var (
		versionDir string
		launchVars LaunchVars
	)

	BeforeEach(func() {
		versionDir = GinkgoT().TempDir()
		launchVars = LaunchVars{
			Version:     "v1",
			VersionDir:  versionDir,
			StateDir:    "/tmp/state/hello",
			MonitorPort: 18090,
			KillSock:    "/tmp/state/hello/kill.sock",
		}
	})

	writeFile := func(rel, body string, mode os.FileMode) string {
		path := filepath.Join(versionDir, rel)
		Expect(os.MkdirAll(filepath.Dir(path), 0755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(body), mode)).To(Succeed())
		return path
	}

	It("validates manifest validate_templates without writing rendered files", func() {
		writeFile("manifest.yml", `validate_templates:
  - config.yml
default_vars:
  VALUE: ok
`, 0644)
		writeFile("config.yml.tmpl", `value: "{{ .VALUE }}"`, 0644)

		err := validateVersionTemplatesWithEnv(versionDir, nil, nil, launchVars)
		Expect(err).NotTo(HaveOccurred())
		_, statErr := os.Stat(filepath.Join(versionDir, "config.yml"))
		Expect(os.IsNotExist(statErr)).To(BeTrue())
	})

	It("validates real config files listed in validate_templates", func() {
		writeFile("manifest.yml", `validate_templates:
  - config.yml
default_vars:
  VALUE: ok
`, 0644)
		writeFile("config.yml", `value: "{{ .VALUE }}"`, 0644)

		err := validateVersionTemplatesWithEnv(versionDir, nil, nil, launchVars)
		Expect(err).NotTo(HaveOccurred())
	})

	It("falls back to legacy .tmpl files when the listed file is absent", func() {
		writeFile("manifest.yml", `validate_templates:
  - config.yml
default_vars:
  VALUE: ok
`, 0644)
		writeFile("config.yml.tmpl", `value: "{{ .VALUE }}"`, 0644)

		err := validateVersionTemplatesWithEnv(versionDir, nil, nil, launchVars)
		Expect(err).NotTo(HaveOccurred())
	})

	It("uses manifest default_vars for validation", func() {
		writeFile("manifest.yml", `validate_templates:
  - config.yml
default_vars:
  LOG_LEVEL: info
`, 0644)
		writeFile("config.yml.tmpl", `level: "{{ .LOG_LEVEL }}"`, 0644)

		err := validateVersionTemplatesWithEnv(versionDir, nil, nil, launchVars)
		Expect(err).NotTo(HaveOccurred())
	})

	It("allows supervisor vars and component env to override default_vars during validation", func() {
		writeFile("manifest.yml", `validate_templates:
  - config.yml
default_vars:
  TOKEN: from default
`, 0644)
		writeFile("config.yml.tmpl", `value: "{{ .TOKEN }}"
from_env_func: "{{ env "TOKEN" }}"
`, 0644)

		err := validateVersionTemplatesWithEnv(versionDir, map[string]any{"TOKEN": "from supervisor"}, map[string]string{"TOKEN": "from component"}, launchVars)
		Expect(err).NotTo(HaveOccurred())
	})

	It("exposes launch vars and artifact-compatible aliases to validation", func() {
		GinkgoT().Setenv("BUILD_DIR", "/from/process/env")
		writeFile("manifest.yml", "validate_templates:\n  - paths.toml\n", 0644)
		writeFile("paths.toml.tmpl", `dir = "{{ .VERSION_DIR }}"; build = "{{ .BUILD_DIR }}"; builddir = "{{ .BUILDDIR }}"; required = "{{ .REQUIRED_VERSION }}"; port = "{{ .MONITOR_PORT }}"`, 0644)

		err := validateVersionTemplatesWithEnv(versionDir, nil, nil, launchVars)
		Expect(err).NotTo(HaveOccurred())
	})

	It("processes vars sections with the shared runctl config engine", func() {
		writeFile("manifest.yml", "validate_templates:\n  - config.yml\n", 0644)
		writeFile("config.yml.tmpl", `vars:
  BASE_PORT: "8000"
  API_PORT: "{{ add .BASE_PORT 81 }}"
server:
  port: "{{ .API_PORT }}"
`, 0644)

		err := validateVersionTemplatesWithEnv(versionDir, nil, nil, launchVars)
		Expect(err).NotTo(HaveOccurred())
	})

	It("fails validation when a validate_templates entry is missing", func() {
		writeFile("manifest.yml", "validate_templates:\n  - missing.yml\n", 0644)

		err := validateVersionTemplatesWithEnv(versionDir, nil, nil, launchVars)
		Expect(err).To(MatchError(ContainSubstring("manifest.yml validate_templates entry")))
	})

	It("returns useful validation errors for missing variables", func() {
		writeFile("manifest.yml", "validate_templates:\n  - config.toml\n", 0644)
		writeFile("config.toml.tmpl", `x = "{{ .UNDEFINED }}"`, 0644)

		err := validateVersionTemplatesWithEnv(versionDir, nil, nil, launchVars)
		Expect(err).To(MatchError(ContainSubstring("undefined variable")))
		_, statErr := os.Stat(filepath.Join(versionDir, "config.toml"))
		Expect(os.IsNotExist(statErr)).To(BeTrue())
	})

	It("validates rendered YAML and JSON when listed", func() {
		writeFile("manifest.yml", "validate_templates:\n  - bad.yml\n  - bad.json\n", 0644)
		writeFile("bad.yml.tmpl", `name: [unbalanced`, 0644)
		writeFile("bad.json.tmpl", `{"k": missing-closing-brace`, 0644)

		err := validateVersionTemplatesWithEnv(versionDir, nil, nil, launchVars)
		Expect(err).To(MatchError(ContainSubstring("bad.yml")))
	})

	It("does nothing when no validate_templates are listed", func() {
		writeFile("config.toml.tmpl", `x = "{{ .UNDEFINED }}"`, 0644)

		err := validateVersionTemplatesWithEnv(versionDir, nil, nil, launchVars)
		Expect(err).NotTo(HaveOccurred())
	})

	It("keeps legacy flat defaults.yml compatibility for env defaults", func() {
		writeFile("defaults.yml", "LOG_LEVEL: info\n", 0644)
		env, err := buildRenderEnv(versionDir, nil, nil, launchVars)

		Expect(err).NotTo(HaveOccurred())
		Expect(env["LOG_LEVEL"]).To(Equal("info"))
	})

	It("passes the same deployment and launch env shape to child processes", func() {
		GinkgoT().Setenv("TOKEN", "from process")
		GinkgoT().Setenv("BUILD_DIR", "/from/process/env")
		writeFile("manifest.yml", `default_vars:
  TOKEN: from default
  REGION: eu
`, 0644)

		c := &Component{
			cfg: ComponentConfig{
				Env: map[string]string{
					"TOKEN": "from component",
				},
			},
			supervisorVars: map[string]any{
				"TOKEN": "from supervisor",
			},
		}

		items, err := c.buildEnv(launchVars)
		Expect(err).NotTo(HaveOccurred())
		env := environSliceToMap(items)
		Expect(env["TOKEN"]).To(Equal("from component"))
		Expect(env["REGION"]).To(Equal("eu"))
		Expect(env["BUILD_DIR"]).To(Equal(versionDir))
		Expect(env["BUILDDIR"]).To(Equal(versionDir))
		Expect(env["REQUIRED_VERSION"]).To(Equal("v1"))
		Expect(env["OP_VERSION_DIR"]).To(Equal(versionDir))
	})
})
