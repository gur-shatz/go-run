package runctl_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/runctl"
)

var _ = Describe("Config", func() {
	Describe("LoadConfig", func() {
		It("loads a valid execrun config file", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
api:
  port: 9200
targets:
  my-app:
    config: "my-app/execrun.yaml"
    enabled: true
    links:
      - name: "App"
        url: "http://localhost:8080"
  worker:
    config: "worker/execrun.yaml"
    enabled: false
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, err := runctl.LoadConfig(cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.API.Port).To(Equal(9200))
			Expect(cfg.Targets).To(HaveLen(2))
			Expect(cfg.Targets["my-app"].Config).To(Equal("my-app/execrun.yaml"))
			Expect(cfg.Targets["my-app"].IsEnabled()).To(BeTrue())
			Expect(cfg.Targets["my-app"].Links).To(HaveLen(1))
			Expect(cfg.Targets["my-app"].Links[0].Name).To(Equal("App"))
			Expect(cfg.Targets["worker"].IsEnabled()).To(BeFalse())
		})

		It("loads a valid gorun config", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
api:
  port: 9200
targets:
  api-server:
    type: gorun
    config: "services/api/gorun.yaml"
    enabled: true
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, err := runctl.LoadConfig(cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Targets).To(HaveLen(1))
			Expect(cfg.Targets["api-server"].Type).To(Equal("gorun"))
			Expect(cfg.Targets["api-server"].Config).To(Equal("services/api/gorun.yaml"))
		})

		It("loads mixed execrun and gorun targets", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  app:
    type: gorun
    config: "app/gorun.yaml"
  worker:
    type: execrun
    config: "worker/execrun.yaml"
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, err := runctl.LoadConfig(cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Targets).To(HaveLen(2))
			Expect(cfg.Targets["app"].EffectiveType()).To(Equal("gorun"))
			Expect(cfg.Targets["worker"].EffectiveType()).To(Equal("execrun"))
		})

		It("sets default port when not specified", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  my-app:
    config: "execrun.yaml"
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, err := runctl.LoadConfig(cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.API.Port).To(Equal(9100))
		})

		It("returns error for missing config field", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  my-app:
    type: execrun
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = runctl.LoadConfig(cfgPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("config is required"))
		})

		It("returns error for gorun target missing config field", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  my-app:
    type: gorun
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = runctl.LoadConfig(cfgPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("config is required"))
		})

		It("returns error for unknown type", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  my-app:
    type: unknown
    config: "something.yaml"
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = runctl.LoadConfig(cfgPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unknown type"))
		})

		It("returns error when no targets defined", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
api:
  port: 9100
targets:
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = runctl.LoadConfig(cfgPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one target"))
		})

		It("returns error for missing file", func() {
			_, err := runctl.LoadConfig("/nonexistent/runctl.yaml")
			Expect(err).To(HaveOccurred())
		})

		It("defaults type to execrun when omitted", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  my-app:
    config: "execrun.yaml"
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, err := runctl.LoadConfig(cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Targets["my-app"].EffectiveType()).To(Equal("execrun"))
		})
	})

	Describe("Link validation", func() {
		It("accepts a link with url only", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  app:
    config: "app/execrun.yaml"
    links:
      - name: "App"
        url: "http://localhost:8080"
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, err := runctl.LoadConfig(cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Targets["app"].Links[0].URL).To(Equal("http://localhost:8080"))
			Expect(cfg.Targets["app"].Links[0].File).To(BeEmpty())
		})

		It("accepts a link with file only and resolves relative path", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  app:
    config: "app/execrun.yaml"
    links:
      - name: "Config"
        file: "./config.yaml"
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, err := runctl.LoadConfig(cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Targets["app"].Links[0].File).To(Equal(filepath.Join(dir, "config.yaml")))
		})

		It("rejects a link with both url and file", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  app:
    config: "app/execrun.yaml"
    links:
      - name: "Bad"
        url: "http://localhost:8080"
        file: "./config.yaml"
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = runctl.LoadConfig(cfgPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("cannot specify both url and file"))
		})

		It("rejects a link with neither url nor file", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  app:
    config: "app/execrun.yaml"
    links:
      - name: "Empty"
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			_, err = runctl.LoadConfig(cfgPath)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("must specify either url or file"))
		})

		It("keeps absolute file paths unchanged", func() {
			dir := GinkgoT().TempDir()
			cfgPath := filepath.Join(dir, "runctl.yaml")

			yaml := `
targets:
  app:
    config: "app/execrun.yaml"
    links:
      - name: "Config"
        file: "/absolute/path/config.yaml"
`
			err := os.WriteFile(cfgPath, []byte(yaml), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg, err := runctl.LoadConfig(cfgPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Targets["app"].Links[0].File).To(Equal("/absolute/path/config.yaml"))
		})
	})

	Describe("TargetConfig.IsEnabled", func() {
		It("defaults to true when Enabled is nil", func() {
			tc := runctl.TargetConfig{Config: "execrun.yaml"}
			Expect(tc.IsEnabled()).To(BeTrue())
		})

		It("returns false when Enabled is false", func() {
			f := false
			tc := runctl.TargetConfig{Config: "execrun.yaml", Enabled: &f}
			Expect(tc.IsEnabled()).To(BeFalse())
		})

		It("returns true when Enabled is true", func() {
			t := true
			tc := runctl.TargetConfig{Config: "execrun.yaml", Enabled: &t}
			Expect(tc.IsEnabled()).To(BeTrue())
		})
	})

	Describe("TargetConfig.EffectiveType", func() {
		It("defaults to execrun when Type is empty", func() {
			tc := runctl.TargetConfig{}
			Expect(tc.EffectiveType()).To(Equal("execrun"))
		})

		It("returns gorun when Type is gorun", func() {
			tc := runctl.TargetConfig{Type: "gorun"}
			Expect(tc.EffectiveType()).To(Equal("gorun"))
		})

		It("returns execrun when Type is execrun", func() {
			tc := runctl.TargetConfig{Type: "execrun"}
			Expect(tc.EffectiveType()).To(Equal("execrun"))
		})
	})

	Describe("Controller", func() {
		It("creates a controller from valid config", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"test": {Config: "test/execrun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())
			Expect(ctrl).NotTo(BeNil())
		})

		It("creates a controller with gorun targets", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"test": {Type: "gorun", Config: "test/gorun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())
			Expect(ctrl).NotTo(BeNil())
		})

		It("returns status for all targets", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"app1": {Config: "app1/execrun.yaml"},
					"app2": {Type: "gorun", Config: "app2/gorun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())

			statuses := ctrl.Status()
			Expect(statuses).To(HaveLen(2))
		})

		It("returns correct type in status", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"app": {Type: "gorun", Config: "app/gorun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())

			status, err := ctrl.TargetStatus("app")
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Type).To(Equal("gorun"))
		})

		It("returns error for unknown target", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"app": {Config: "app/execrun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())

			_, err = ctrl.TargetStatus("nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("returns idle state for all targets initially", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"app1": {Config: "app1/execrun.yaml"},
					"app2": {Type: "gorun", Config: "app2/gorun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())

			for _, s := range ctrl.Status() {
				Expect(s.State).To(Equal(runctl.StateIdle))
			}
		})

		It("Stop on idle target is safe", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"app": {Config: "app/execrun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())

			Expect(ctrl.StopTarget("app")).To(Succeed())
		})

		It("EnableTarget and DisableTarget toggle the enabled flag", func() {
			f := false
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"app": {Config: "app/execrun.yaml", Enabled: &f},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())

			status, err := ctrl.TargetStatus("app")
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Enabled).To(BeFalse())

			// EnableTarget will try to start, which will fail (no config file),
			// but the enabled flag should still be toggled.
			_ = ctrl.EnableTarget("app")
			status, err = ctrl.TargetStatus("app")
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Enabled).To(BeTrue())

			Expect(ctrl.DisableTarget("app")).To(Succeed())
			status, err = ctrl.TargetStatus("app")
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Enabled).To(BeFalse())
		})

		It("StartTarget on nonexistent target returns error", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"app": {Config: "app/execrun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())

			err = ctrl.StartTarget("nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("StopTarget on nonexistent target returns error", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"app": {Config: "app/execrun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())

			err = ctrl.StopTarget("nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("RestartTarget on nonexistent target returns error", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"app": {Config: "app/execrun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())

			err = ctrl.RestartTarget("nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("BuildTarget on nonexistent target returns error", func() {
			cfg := runctl.Config{
				API: runctl.APIConfig{Port: 9100},
				Targets: map[string]runctl.TargetConfig{
					"app": {Config: "app/execrun.yaml"},
				},
			}
			ctrl, err := runctl.New(cfg, ".")
			Expect(err).NotTo(HaveOccurred())

			err = ctrl.BuildTarget("nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})
})
