package config_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/config"
)

var _ = Describe("Load", func() {
	var tempDir string

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "config-load-test-*")
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tempDir)
	})

	writeConfig := func(yamlContent string) string {
		path := filepath.Join(tempDir, "config.yml")
		err := os.WriteFile(path, []byte(yamlContent), 0644)
		Expect(err).ToNot(HaveOccurred())
		return path
	}

	Describe("Basic loading", func() {
		It("should load a simple config file", func() {
			path := writeConfig(`
name: test-app
port: 8080
`)
			cfg, _, err := config.Load(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("name")).To(Equal("test-app"))
			Expect(cfg.GetString("port")).To(Equal("8080"))
		})

		It("should return error for non-existent file", func() {
			_, _, err := config.Load("/nonexistent/path/config.yml")
			Expect(err).To(HaveOccurred())
		})

		It("should return empty config when path is empty and no config found", func() {
			// Change to temp dir that has no config.yml
			origDir, _ := os.Getwd()
			os.Chdir(tempDir)
			defer os.Chdir(origDir)

			cfg, _, err := config.Load("")
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg).To(BeEmpty())
		})
	})

	Describe("Template variable substitution", func() {
		It("should substitute vars section variables", func() {
			path := writeConfig(`
vars:
  db_host: localhost
  db_port: 5432
database:
  host: "{{ .db_host }}"
  port: "{{ .db_port }}"
`)
			cfg, _, err := config.Load(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("database.host")).To(Equal("localhost"))
			Expect(cfg.GetString("database.port")).To(Equal("5432"))
		})

		It("should remove vars section from final config", func() {
			path := writeConfig(`
vars:
  foo: bar
value: "{{ .foo }}"
`)
			cfg, _, err := config.Load(path)
			Expect(err).ToNot(HaveOccurred())
			_, hasVars := cfg.Get("vars")
			Expect(hasVars).To(BeFalse())
			Expect(cfg.GetString("value")).To(Equal("bar"))
		})

		It("should return resolved vars", func() {
			path := writeConfig(`
vars:
  app_name: myapp
  app_port: "9090"
name: "{{ .app_name }}"
`)
			_, vars, err := config.Load(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(vars["app_name"]).To(Equal("myapp"))
			Expect(vars["app_port"]).To(Equal("9090"))
		})
	})

	Describe("Alternative [[ ]] syntax", func() {
		It("should substitute using [[ ]] delimiters", func() {
			path := writeConfig(`
vars:
  host: example.com
server:
  address: "[[ .host ]]"
`)
			cfg, _, err := config.Load(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("server.address")).To(Equal("example.com"))
		})
	})

	Describe("Recursive variable definitions", func() {
		It("should handle vars referencing other vars", func() {
			path := writeConfig(`
vars:
  base_path: /opt/app
  db_path: "{{ .base_path }}/data"
  log_path: "{{ .base_path }}/logs"
database:
  path: "{{ .db_path }}"
logging:
  path: "{{ .log_path }}"
`)
			cfg, _, err := config.Load(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("database.path")).To(Equal("/opt/app/data"))
			Expect(cfg.GetString("logging.path")).To(Equal("/opt/app/logs"))
		})
	})

	Describe("Environment variable precedence", func() {
		BeforeEach(func() {
			os.Setenv("TEST_VAR", "from_env")
		})

		AfterEach(func() {
			os.Unsetenv("TEST_VAR")
		})

		It("should prefer env var over vars section", func() {
			path := writeConfig(`
vars:
  TEST_VAR: from_vars
value: "{{ .TEST_VAR }}"
`)
			cfg, _, err := config.Load(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("value")).To(Equal("from_env"))
		})

		It("should use vars section when env var not set", func() {
			path := writeConfig(`
vars:
  MY_VAR: from_vars
value: "{{ .MY_VAR }}"
`)
			cfg, _, err := config.Load(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("value")).To(Equal("from_vars"))
		})
	})

	Describe("Template functions", func() {
		It("should use default when variable not defined", func() {
			path := writeConfig(`
database:
  timeout: '{{ .DB_TIMEOUT | default "30" }}'
`)
			cfg, _, err := config.Load(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("database.timeout")).To(Equal("30"))
		})

		It("should use value when variable is defined", func() {
			os.Setenv("DB_TIMEOUT", "60")
			defer os.Unsetenv("DB_TIMEOUT")

			path := writeConfig(`
database:
  timeout: '{{ .DB_TIMEOUT | default "30" }}'
`)
			cfg, _, err := config.Load(path)
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("database.timeout")).To(Equal("60"))
		})
	})

	Describe("WithLoadEnv option", func() {
		It("should use custom env map instead of os.Environ", func() {
			path := writeConfig(`
value: "{{ .CUSTOM_VAR }}"
`)
			customEnv := map[string]string{
				"CUSTOM_VAR": "custom_value",
			}
			cfg, _, err := config.Load(path, config.WithLoadEnv(customEnv))
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("value")).To(Equal("custom_value"))
		})

		It("should not see os.Environ when custom env is provided", func() {
			os.Setenv("OS_VAR", "from_os")
			defer os.Unsetenv("OS_VAR")

			path := writeConfig(`
value: '{{ .OS_VAR | default "not_found" }}'
`)
			customEnv := map[string]string{}
			cfg, _, err := config.Load(path, config.WithLoadEnv(customEnv))
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("value")).To(Equal("not_found"))
		})
	})

	Describe("vars.yml sidecar", func() {
		It("should load vars from vars.yml in same directory", func() {
			// Write vars.yml
			varsPath := filepath.Join(tempDir, "vars.yml")
			err := os.WriteFile(varsPath, []byte("SIDECAR_VAR: from_sidecar\n"), 0644)
			Expect(err).ToNot(HaveOccurred())

			path := writeConfig(`
value: "{{ .SIDECAR_VAR }}"
`)
			cfg, _, err := config.Load(path, config.WithLoadEnv(map[string]string{}))
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("value")).To(Equal("from_sidecar"))
		})

		It("should prefer env vars over vars.yml", func() {
			varsPath := filepath.Join(tempDir, "vars.yml")
			err := os.WriteFile(varsPath, []byte("MY_VAR: from_sidecar\n"), 0644)
			Expect(err).ToNot(HaveOccurred())

			path := writeConfig(`
value: "{{ .MY_VAR }}"
`)
			cfg, _, err := config.Load(path, config.WithLoadEnv(map[string]string{
				"MY_VAR": "from_env",
			}))
			Expect(err).ToNot(HaveOccurred())
			Expect(cfg.GetString("value")).To(Equal("from_env"))
		})
	})

	Describe("Error handling", func() {
		It("should error on undefined variable without default", func() {
			path := writeConfig(`
value: "{{ .UNDEFINED_VAR }}"
`)
			_, _, err := config.Load(path)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("undefined variable"))
		})
	})

	Describe("ProcessVariables backward compat", func() {
		It("should remove vars section", func() {
			cfg := config.O{
				"vars": map[string]any{
					"foo": "bar",
				},
				"value": "test",
			}
			result, err := config.ProcessVariables(cfg)
			Expect(err).ToNot(HaveOccurred())
			_, hasVars := result.Get("vars")
			Expect(hasVars).To(BeFalse())
		})

		It("should handle nil config", func() {
			var cfg config.O
			result, err := config.ProcessVariables(cfg)
			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(BeEmpty())
		})
	})
})
