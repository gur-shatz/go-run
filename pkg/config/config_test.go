package config_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/config"
)

var _ = Describe("Config", func() {
	Describe("Process", func() {
		It("passes through YAML without templates", func() {
			input := []byte("name: hello\nport: 8080\n")
			result, vars, err := config.Process(input, config.WithEnv(map[string]string{}))
			Expect(err).NotTo(HaveOccurred())
			Expect(vars).To(BeEmpty())
			Expect(string(result)).To(ContainSubstring("name: hello"))
			Expect(string(result)).To(ContainSubstring("port: 8080"))
		})

		It("resolves vars section", func() {
			input := []byte(`
vars:
  app_name: myapp
  app_port: "9090"
name: "{{ .app_name }}"
port: "{{ .app_port }}"
`)
			result, vars, err := config.Process(input, config.WithEnv(map[string]string{}))
			Expect(err).NotTo(HaveOccurred())
			Expect(vars["app_name"]).To(Equal("myapp"))
			Expect(vars["app_port"]).To(Equal("9090"))
			Expect(string(result)).To(ContainSubstring("name: myapp"))
			Expect(string(result)).To(ContainSubstring("port: \"9090\""))
			Expect(string(result)).NotTo(ContainSubstring("vars:"))
		})

		It("env vars take precedence over vars section", func() {
			input := []byte(`
vars:
  MY_PORT: "3000"
port: "{{ .MY_PORT }}"
`)
			result, _, err := config.Process(input, config.WithEnv(map[string]string{
				"MY_PORT": "5000",
			}))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(result)).To(ContainSubstring("port: \"5000\""))
		})

		It("WithVars provides additional variables", func() {
			input := []byte(`name: "{{ .custom_var }}"`)
			result, _, err := config.Process(input,
				config.WithEnv(map[string]string{}),
				config.WithVars(map[string]string{"custom_var": "hello"}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(result)).To(ContainSubstring("name: hello"))
		})

		It("env vars take precedence over WithVars", func() {
			input := []byte(`name: "{{ .MY_VAR }}"`)
			result, _, err := config.Process(input,
				config.WithEnv(map[string]string{"MY_VAR": "from_env"}),
				config.WithVars(map[string]string{"MY_VAR": "from_vars"}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(result)).To(ContainSubstring("name: from_env"))
		})

		Context("template functions", func() {
			It("default returns fallback for missing var", func() {
				input := []byte(`port: "{{ .MISSING | default "8080" }}"`)
				result, _, err := config.Process(input, config.WithEnv(map[string]string{}))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(result)).To(ContainSubstring("port: \"8080\""))
			})

			It("default returns actual value when present", func() {
				input := []byte(`port: "{{ .PORT | default "8080" }}"`)
				result, _, err := config.Process(input, config.WithEnv(map[string]string{"PORT": "3000"}))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(result)).To(ContainSubstring("port: \"3000\""))
			})

			It("required fails for missing var", func() {
				input := []byte(`password: "{{ .DB_PASS | required "DB_PASS must be set" }}"`)
				_, _, err := config.Process(input, config.WithEnv(map[string]string{}))
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("DB_PASS must be set"))
			})

			It("required passes for present var", func() {
				input := []byte(`password: "{{ .DB_PASS | required "DB_PASS must be set" }}"`)
				result, _, err := config.Process(input, config.WithEnv(map[string]string{"DB_PASS": "secret"}))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(result)).To(ContainSubstring("password: secret"))
			})

			It("env function looks up env var directly", func() {
				input := []byte(`home: '{{ env "MY_HOME" }}'`)
				result, _, err := config.Process(input, config.WithEnv(map[string]string{"MY_HOME": "/users/test"}))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(result)).To(ContainSubstring("home: /users/test"))
			})

			It("asInt casts a string to integer", func() {
				input := []byte(`
vars:
  port: "8080"
port: {{ .port | asInt }}
`)
				result, _, err := config.Process(input, config.WithEnv(map[string]string{}))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(result)).To(ContainSubstring("port: 8080"))
				Expect(string(result)).NotTo(ContainSubstring(`"8080"`))
			})

			It("add performs integer addition", func() {
				input := []byte(`
vars:
  base: "100"
result: "{{ add .base 5 }}"
`)
				result, _, err := config.Process(input, config.WithEnv(map[string]string{}))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(result)).To(ContainSubstring("result: \"105\""))
			})
		})

		It("resolves recursive vars", func() {
			input := []byte(`
vars:
  base_path: /opt/app
  data_path: "{{ .base_path }}/data"
  db_path: "{{ .data_path }}/db"
result: "{{ .db_path }}"
`)
			result, vars, err := config.Process(input, config.WithEnv(map[string]string{}))
			Expect(err).NotTo(HaveOccurred())
			Expect(vars["db_path"]).To(Equal("/opt/app/data/db"))
			Expect(string(result)).To(ContainSubstring("result: /opt/app/data/db"))
		})

		It("returns error for unresolved variables", func() {
			input := []byte(`name: "{{ .UNDEFINED_VAR }}"`)
			_, _, err := config.Process(input, config.WithEnv(map[string]string{}))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("undefined variable"))
		})

		It("supports [[ ]] delimiters", func() {
			input := []byte(`name: "[[ .app_name ]]"
`)
			result, _, err := config.Process(input,
				config.WithEnv(map[string]string{}),
				config.WithVars(map[string]string{"app_name": "myapp"}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(result)).To(ContainSubstring("name: myapp"))
		})

		It("supports mixed {{ }} and [[ ]] delimiters", func() {
			input := []byte(`
vars:
  port: "3000"
name: "[[ .app_name ]]"
port: "{{ .port }}"
`)
			result, _, err := config.Process(input,
				config.WithEnv(map[string]string{}),
				config.WithVars(map[string]string{"app_name": "myapp"}),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(result)).To(ContainSubstring("name: myapp"))
			Expect(string(result)).To(ContainSubstring("port: \"3000\""))
		})
	})

	Describe("ProcessFile", func() {
		It("reads and processes a file", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "test.yaml")
			err := os.WriteFile(cfgPath, []byte(`
vars:
  greeting: hello
message: "{{ .greeting }} world"
`), 0644)
			Expect(err).NotTo(HaveOccurred())

			result, vars, err := config.ProcessFile(cfgPath, config.WithEnv(map[string]string{}))
			Expect(err).NotTo(HaveOccurred())
			Expect(vars["greeting"]).To(Equal("hello"))
			Expect(string(result)).To(ContainSubstring("message: hello world"))
		})

		It("returns error for missing file", func() {
			_, _, err := config.ProcessFile("/nonexistent/file.yaml")
			Expect(err).To(HaveOccurred())
		})
	})
})
