package gorun_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/gorun"
)

var _ = Describe("Config", func() {
	Describe("LoadConfig", func() {
		It("loads a valid config with args only", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "gorun.yaml")

			yaml := `args: "./cmd/server -port 8080"
`
			Expect(os.WriteFile(cfgPath, []byte(yaml), 0644)).To(Succeed())

			cfg, _, err := gorun.LoadConfig(cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Args).To(Equal("./cmd/server -port 8080"))
			Expect(cfg.Watch).To(BeEmpty())
			Expect(cfg.Exec).To(BeEmpty())
		})

		It("loads a config with all fields", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "gorun.yaml")

			yaml := `watch:
  - "**/*.go"
  - "go.mod"
  - "!vendor/**"
args: "-race ./cmd/server -port 8080"
exec:
  - "go generate ./..."
  - "echo done"
`
			Expect(os.WriteFile(cfgPath, []byte(yaml), 0644)).To(Succeed())

			cfg, _, err := gorun.LoadConfig(cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Watch).To(Equal([]string{"**/*.go", "go.mod", "!vendor/**"}))
			Expect(cfg.Args).To(Equal("-race ./cmd/server -port 8080"))
			Expect(cfg.Exec).To(Equal([]string{"go generate ./...", "echo done"}))
		})

		It("returns error when args is missing", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "gorun.yaml")

			yaml := `watch:
  - "**/*.go"
`
			Expect(os.WriteFile(cfgPath, []byte(yaml), 0644)).To(Succeed())

			_, _, err := gorun.LoadConfig(cfgPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("args is required"))
		})

		It("returns error for missing file", func() {
			_, _, err := gorun.LoadConfig("/nonexistent/gorun.yaml")
			Expect(err).To(HaveOccurred())
		})

		It("returns error for invalid YAML", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "gorun.yaml")

			Expect(os.WriteFile(cfgPath, []byte("{{invalid yaml"), 0644)).To(Succeed())

			_, _, err := gorun.LoadConfig(cfgPath)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("ParseArgs", func() {
		It("parses simple build target", func() {
			cfg := &gorun.Config{Args: "."}
			flags, target, appArgs := cfg.ParseArgs()
			Expect(flags).To(BeEmpty())
			Expect(target).To(Equal("."))
			Expect(appArgs).To(BeEmpty())
		})

		It("parses build target with app args", func() {
			cfg := &gorun.Config{Args: "./cmd/server -port 8080"}
			flags, target, appArgs := cfg.ParseArgs()
			Expect(flags).To(BeEmpty())
			Expect(target).To(Equal("./cmd/server"))
			Expect(appArgs).To(Equal([]string{"-port", "8080"}))
		})

		It("parses build flags + target + app args", func() {
			cfg := &gorun.Config{Args: "-race ./cmd/server -port 8080"}
			flags, target, appArgs := cfg.ParseArgs()
			Expect(flags).To(Equal([]string{"-race"}))
			Expect(target).To(Equal("./cmd/server"))
			Expect(appArgs).To(Equal([]string{"-port", "8080"}))
		})

		It("parses build flags with values", func() {
			cfg := &gorun.Config{Args: "-tags integration ./cmd/server"}
			flags, target, appArgs := cfg.ParseArgs()
			Expect(flags).To(Equal([]string{"-tags", "integration"}))
			Expect(target).To(Equal("./cmd/server"))
			Expect(appArgs).To(BeEmpty())
		})

		It("returns empty for empty args", func() {
			cfg := &gorun.Config{Args: ""}
			flags, target, appArgs := cfg.ParseArgs()
			Expect(flags).To(BeNil())
			Expect(target).To(BeEmpty())
			Expect(appArgs).To(BeNil())
		})

		It("parses -ldflags with double-quoted value containing spaces", func() {
			cfg := &gorun.Config{Args: `-ldflags="-X pkg.Branch=main -X pkg.Commit=abc123" cmd/server/main.go -c ../config.yml`}
			flags, target, appArgs := cfg.ParseArgs()
			Expect(flags).To(Equal([]string{"-ldflags=-X pkg.Branch=main -X pkg.Commit=abc123"}))
			Expect(target).To(Equal("cmd/server/main.go"))
			Expect(appArgs).To(Equal([]string{"-c", "../config.yml"}))
		})

		It("parses -ldflags with separate quoted value", func() {
			cfg := &gorun.Config{Args: `-ldflags "-X pkg.Branch=main -X pkg.Commit=abc" ./cmd/server`}
			flags, target, appArgs := cfg.ParseArgs()
			Expect(flags).To(Equal([]string{"-ldflags", "-X pkg.Branch=main -X pkg.Commit=abc"}))
			Expect(target).To(Equal("./cmd/server"))
			Expect(appArgs).To(BeEmpty())
		})

		It("parses single-quoted values", func() {
			cfg := &gorun.Config{Args: `-ldflags='-X pkg.A=1 -X pkg.B=2' ./cmd/server`}
			flags, target, appArgs := cfg.ParseArgs()
			Expect(flags).To(Equal([]string{"-ldflags=-X pkg.A=1 -X pkg.B=2"}))
			Expect(target).To(Equal("./cmd/server"))
			Expect(appArgs).To(BeEmpty())
		})
	})

	Describe("ParseWatchPatterns", func() {
		It("converts string patterns to glob patterns", func() {
			patterns := gorun.ParseWatchPatterns([]string{"**/*.go", "!vendor/**", "go.mod"})
			Expect(patterns).To(HaveLen(3))
			Expect(patterns[0].Raw).To(Equal("**/*.go"))
			Expect(patterns[0].Negated).To(BeFalse())
			Expect(patterns[1].Raw).To(Equal("vendor/**"))
			Expect(patterns[1].Negated).To(BeTrue())
			Expect(patterns[2].Raw).To(Equal("go.mod"))
			Expect(patterns[2].Negated).To(BeFalse())
		})
	})
})
