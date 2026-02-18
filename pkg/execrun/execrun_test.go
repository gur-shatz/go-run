package execrun_test

import (
	"os"
	"path/filepath"

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
			content := `watch:
  - "**/*.go"
  - "!vendor/**"
build:
  - "go build -o ./bin/app ."
exec:
  - "./bin/app"
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Watch).To(Equal([]string{"**/*.go", "!vendor/**"}))
			Expect(cfg.Build).To(Equal([]string{"go build -o ./bin/app ."}))
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

			cfg, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Steps()).To(Equal([]string{
				"protoc --go_out=. api/*.proto",
				"go generate ./...",
				"make build",
			}))
			Expect(cfg.RunCmd()).To(Equal("./bin/server"))
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

			cfg, err := execrun.LoadConfig(configPath)
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

			cfg, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.IsBuildOnly()).To(BeTrue())
			Expect(cfg.Steps()).To(Equal([]string{"npm run build"}))
			Expect(cfg.RunCmd()).To(Equal(""))
		})

		It("returns error for empty build and exec", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `watch:
  - "**/*.go"
build: []
exec: []
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = execrun.LoadConfig(configPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one build or exec command"))
		})

		It("returns error for missing build and exec fields", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `watch:
  - "**/*.go"
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = execrun.LoadConfig(configPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one build or exec command"))
		})

		It("returns error for empty watch field", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := `watch: []
exec:
  - "./app"
`
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = execrun.LoadConfig(configPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("watch must have at least one pattern"))
		})

		It("returns error for missing config file", func() {
			_, err := execrun.LoadConfig(filepath.Join(tmpDir, "nonexistent.yaml"))
			Expect(err).To(HaveOccurred())
		})

		It("returns error for invalid YAML", func() {
			configPath := filepath.Join(tmpDir, "bad.yaml")
			err := os.WriteFile(configPath, []byte("{{invalid yaml"), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = execrun.LoadConfig(configPath)
			Expect(err).To(HaveOccurred())
		})

		It("trims whitespace from YAML literal blocks", func() {
			configPath := filepath.Join(tmpDir, "execrun.yaml")
			content := "watch:\n  - \"**/*.go\"\nbuild:\n  - |\n    go build .\nexec:\n  - |\n    ./app\n"
			err := os.WriteFile(configPath, []byte(content), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, err := execrun.LoadConfig(configPath)
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

			loaded, err := execrun.LoadConfig(configPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(loaded.Watch).To(Equal(cfg.Watch))
			Expect(loaded.Build).To(Equal(cfg.Build))
			Expect(loaded.Exec).To(Equal(cfg.Exec))
		})
	})

	Describe("DefaultConfig", func() {
		It("returns a valid config", func() {
			cfg := execrun.DefaultConfig()
			Expect(cfg.Validate()).NotTo(HaveOccurred())
			Expect(cfg.Watch).NotTo(BeEmpty())
			Expect(cfg.Build).NotTo(BeEmpty())
			Expect(cfg.Exec).NotTo(BeEmpty())
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

		It("rejects config with no build or exec commands", func() {
			cfg := &execrun.Config{
				Watch: []string{"*.go"},
			}
			Expect(cfg.Validate()).To(HaveOccurred())
		})
	})
})
