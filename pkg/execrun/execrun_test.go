package execrun_test

import (
	"os"
	"path/filepath"

	"github.com/google/shlex"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/execrun"
)

var _ = Describe("Execrun", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "execrun-test-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	Describe("LoadConfig", func() {
		It("loads a valid YAML config with build and exec", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `title: "Hello App"
description: "Main HTTP service"
watch:
  - "**/*.go"
  - "!vendor/**"
build:
  - "go build -o ./bin/app ."
exec:
  - "./bin/app"
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, _, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Title).To(Equal("Hello App"))
			Expect(cfg.Description).To(Equal("Main HTTP service"))
			Expect(cfg.Watch).To(Equal([]string{"**/*.go", "!vendor/**"}))
			Expect(cfg.Build).To(Equal([]string{"go build -o ./bin/app ."}))
			Expect(cfg.Test).To(BeNil())
			Expect(cfg.Exec).To(Equal([]string{"./bin/app"}))
			Expect(cfg.Steps()).To(Equal([]string{"go build -o ./bin/app ."}))
			Expect(cfg.RunCmd()).To(Equal("./bin/app"))
		})

		It("loads config with multiple build steps", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `watch:
  - "**/*.go"
build:
  - "protoc --go_out=. api/*.proto"
  - "go generate ./..."
  - "make build"
exec:
  - "./bin/server"
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, _, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Steps()).To(Equal([]string{
				"protoc --go_out=. api/*.proto",
				"go generate ./...",
				"make build",
			}))
			Expect(cfg.RunCmd()).To(Equal("./bin/server"))
		})

		It("loads config with test steps", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `watch:
  - "**/*.go"
build:
  - "go build ./..."
test:
  - "go test ./..."
exec:
  - "./bin/server"
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, _, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.TestSteps()).To(Equal([]string{"go test ./..."}))
			Expect(cfg.Steps()).To(Equal([]string{
				"go build ./...",
				"go test ./...",
			}))
		})

		It("loads config with only an exec command (no build steps)", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `watch:
  - "**/*.py"
exec:
  - "python app.py"
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, _, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Steps()).To(BeNil())
			Expect(cfg.RunCmd()).To(Equal("python app.py"))
		})

		It("loads build-only config (no exec)", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `watch:
  - "*.css"
build:
  - "npm run build"
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, _, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.IsBuildOnly()).To(BeTrue())
			Expect(cfg.Steps()).To(Equal([]string{"npm run build"}))
			Expect(cfg.RunCmd()).To(Equal(""))
		})

		It("returns error for empty build, test, and exec", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `watch:
  - "**/*.go"
build: []
test: []
exec: []
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, _, err = execrun.LoadConfig(configPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one build, test, or exec command"))
		})

		It("returns error for missing build and exec fields", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `watch:
  - "**/*.go"
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, _, err = execrun.LoadConfig(configPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one build, test, or exec command"))
		})

		It("returns error for empty watch field", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `watch: []
exec:
  - "./app"
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, _, err = execrun.LoadConfig(configPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("watch must have at least one pattern"))
		})

		It("returns error for missing config file", func() {
			_, _, err := execrun.LoadConfig(filepath.Join(tmpDir, "nonexistent.yaml"))
			Expect(err).To(HaveOccurred())
		})

		It("returns error for invalid YAML", func() {
			configPath := filepath.Join(tmpDir, "bad.yaml")
			err := os.WriteFile(configPath, []byte("{{invalid yaml"), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, _, err = execrun.LoadConfig(configPath)
			Expect(err).To(HaveOccurred())
		})

		It("trims whitespace from YAML literal blocks", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := "watch:\n  - \"**/*.go\"\nbuild:\n  - |\n    go build .\nexec:\n  - |\n    ./app\n"
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, _, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Build).To(Equal([]string{"go build ."}))
			Expect(cfg.Exec).To(Equal([]string{"./app"}))
		})
	})

	Describe("WriteConfig", func() {
		It("writes and reads back a config", func() {
			configPath := filepath.Join(tmpDir, "out.yaml")
			cfg := execrun.Config{
				Watch: []string{"**/*.py"},
				Build: []string{"lint", "make"},
				Exec:  []string{"./app"},
			}

			err := execrun.WriteConfig(configPath, cfg)
			Expect(err).NotTo(HaveOccurred())

			loaded, _, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.Watch).To(Equal(cfg.Watch))
			Expect(loaded.Build).To(Equal(cfg.Build))
			Expect(loaded.Test).To(Equal(cfg.Test))
			Expect(loaded.Exec).To(Equal(cfg.Exec))
		})
	})

	Describe("DefaultConfig", func() {
		It("returns a valid config", func() {
			cfg := execrun.DefaultConfig()
			Expect(cfg.Validate()).NotTo(HaveOccurred())
			Expect(cfg.Title).NotTo(BeEmpty())
			Expect(cfg.Description).NotTo(BeEmpty())
			Expect(cfg.Watch).NotTo(BeEmpty())
			Expect(cfg.Build).NotTo(BeEmpty())
			Expect(cfg.Test).NotTo(BeEmpty())
			Expect(cfg.Exec).NotTo(BeEmpty())
		})
	})

	Describe("Command Parsing (shlex)", func() {
		It("splits a simple command", func() {
			args, err := shlex.Split("go build .")
			Expect(err).NotTo(HaveOccurred())
			Expect(args).To(Equal([]string{"go", "build", "."}))
		})

		It("splits a command with flags", func() {
			args, err := shlex.Split("go build -o ./bin/app .")
			Expect(err).NotTo(HaveOccurred())
			Expect(args).To(Equal([]string{"go", "build", "-o", "./bin/app", "."}))
		})

		It("handles double-quoted arguments", func() {
			args, err := shlex.Split(`echo "hello world"`)
			Expect(err).NotTo(HaveOccurred())
			Expect(args).To(Equal([]string{"echo", "hello world"}))
		})

		It("handles single-quoted arguments", func() {
			args, err := shlex.Split(`echo 'hello world'`)
			Expect(err).NotTo(HaveOccurred())
			Expect(args).To(Equal([]string{"echo", "hello world"}))
		})

		It("returns empty slice for empty string", func() {
			args, err := shlex.Split("")
			Expect(err).NotTo(HaveOccurred())
			Expect(args).To(BeEmpty())
		})

		It("returns error for unterminated quote", func() {
			_, err := shlex.Split(`echo "hello`)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Validate", func() {
		It("accepts config with watch and single exec command", func() {
			cfg := &execrun.Config{
				Watch: []string{"*.go"},
				Exec:  []string{"./app"},
			}
			Expect(cfg.Validate()).NotTo(HaveOccurred())
		})

		It("accepts config with watch and build-only", func() {
			cfg := &execrun.Config{
				Watch: []string{"*.go"},
				Build: []string{"make"},
			}
			Expect(cfg.Validate()).NotTo(HaveOccurred())
		})

		It("rejects config with no watch patterns", func() {
			cfg := &execrun.Config{
				Exec: []string{"./app"},
			}
			Expect(cfg.Validate()).To(HaveOccurred())
		})

		It("rejects config with no build, test, or exec commands", func() {
			cfg := &execrun.Config{
				Watch: []string{"*.go"},
			}
			Expect(cfg.Validate()).To(HaveOccurred())
		})

		It("accepts config with test-only", func() {
			cfg := &execrun.Config{
				Watch: []string{"*.go"},
				Test:  []string{"go test ./..."},
			}
			Expect(cfg.Validate()).NotTo(HaveOccurred())
		})

		It("rejects build command with $VAR syntax", func() {
			cfg := &execrun.Config{
				Watch: []string{"*.go"},
				Build: []string{"echo $MY_VAR"},
			}
			err := cfg.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("shell variable syntax"))
			Expect(err.Error()).To(ContainSubstring("Go template syntax"))
		})

		It("rejects exec command with ${VAR} syntax", func() {
			cfg := &execrun.Config{
				Watch: []string{"*.go"},
				Exec:  []string{"./app --port=${PORT}"},
			}
			err := cfg.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("shell variable syntax"))
		})

		It("rejects command with $(...) substitution", func() {
			cfg := &execrun.Config{
				Watch: []string{"*.go"},
				Build: []string{"echo $(date)"},
			}
			err := cfg.Validate()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("shell variable syntax"))
		})

		It("accepts commands without shell variable syntax", func() {
			cfg := &execrun.Config{
				Watch: []string{"*.go"},
				Build: []string{"go build -o ./bin/app ."},
				Exec:  []string{"./bin/app --port=8080"},
			}
			Expect(cfg.Validate()).NotTo(HaveOccurred())
		})
	})
})
