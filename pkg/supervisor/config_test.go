package supervisor_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("Config", func() {
	Describe("ApplyDefaults", func() {
		It("fills in all documented defaults on a zero Config", func() {
			cfg := supervisor.Config{}
			cfg.ApplyDefaults()

			Expect(cfg.StateDir).To(Equal("/var/lib/go-run"))
			Expect(cfg.StabilityTime).To(Equal(5 * time.Minute))
			Expect(cfg.CrashWindow).To(Equal(time.Minute))
			Expect(cfg.CrashThreshold).To(Equal(3))
			Expect(cfg.RejectExpiry).To(BeZero()) // no autonomous clear unless set
			Expect(cfg.ExecFailThreshold).To(Equal(2))
			Expect(cfg.KillGracePeriod).To(Equal(5 * time.Second))
			Expect(cfg.VersionFolderRetention).To(Equal(2))
			Expect(cfg.Supervisor.BindAddress).To(Equal("127.0.0.1:9090"))
			Expect(cfg.Remote.Target).To(Equal("required.txt"))
			Expect(cfg.Remote.PollingInterval).To(Equal(time.Minute))
		})

		It("does not overwrite values set by the user", func() {
			cfg := supervisor.Config{
				StateDir:          "/tmp/go-run",
				StabilityTime:     1 * time.Minute,
				CrashThreshold:    7,
				ExecFailThreshold: 4,
			}
			cfg.ApplyDefaults()

			Expect(cfg.StateDir).To(Equal("/tmp/go-run"))
			Expect(cfg.StabilityTime).To(Equal(1 * time.Minute))
			Expect(cfg.CrashThreshold).To(Equal(7))
			Expect(cfg.ExecFailThreshold).To(Equal(4))
		})

		It("inherits remote settings into each component", func() {
			cfg := supervisor.Config{
				Remote: supervisor.RemoteConfig{
					BaseURL:                "https://updates.example.com",
					SignaturePublicKeyPath: "/etc/go-run/update.pub",
				},
				Components: []supervisor.ComponentConfig{
					{Name: "x", Port: 8080, Command: "/bin/x"},
				},
			}
			cfg.ApplyDefaults()

			Expect(cfg.Components[0].Remote.BaseURL).To(Equal("https://updates.example.com"))
			Expect(cfg.Components[0].Remote.SignaturePublicKeyPath).To(Equal("/etc/go-run/update.pub"))
			Expect(cfg.Components[0].Remote.Target).To(Equal("required.txt"))
			Expect(cfg.Components[0].Remote.PollingInterval).To(Equal(time.Minute))
		})

		It("lets a per-component remote override the top-level remote", func() {
			cfg := supervisor.Config{
				Remote: supervisor.RemoteConfig{
					BaseURL:         "https://updates.example.com",
					PollingInterval: time.Minute,
				},
				Components: []supervisor.ComponentConfig{
					{
						Name:    "x",
						Port:    8080,
						Command: "/bin/x",
						Remote: supervisor.RemoteConfig{
							BaseURL:         "https://other.example.com",
							PollingInterval: 30 * time.Second,
						},
					},
				},
			}
			cfg.ApplyDefaults()

			Expect(cfg.Components[0].Remote.BaseURL).To(Equal("https://other.example.com"))
			Expect(cfg.Components[0].Remote.PollingInterval).To(Equal(30 * time.Second))
		})

		It("fills default URL paths on each component", func() {
			cfg := supervisor.Config{
				Remote:     supervisor.RemoteConfig{BaseURL: "https://x"},
				Components: []supervisor.ComponentConfig{{Name: "x", Port: 8080, Command: "/bin/x"}},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Components[0].URLs.Healthz).To(Equal("/healthz"))
			Expect(cfg.Components[0].URLs.Readyz).To(Equal("/readyz"))
			Expect(cfg.Components[0].URLs.State).To(Equal("/state"))
			Expect(cfg.Components[0].URLs.Metrics).To(Equal("/metrics"))
		})

		It("preserves explicitly set URL paths", func() {
			cfg := supervisor.Config{
				Remote: supervisor.RemoteConfig{BaseURL: "https://x"},
				Components: []supervisor.ComponentConfig{
					{Name: "x", Port: 8080, Command: "/bin/x", URLs: supervisor.URLsConfig{Healthz: "/health"}},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Components[0].URLs.Healthz).To(Equal("/health"))
			Expect(cfg.Components[0].URLs.Readyz).To(Equal("/readyz"))
		})
	})

	Describe("Validate", func() {
		It("requires every component to have a name", func() {
			cfg := supervisor.Config{
				Remote:     supervisor.RemoteConfig{BaseURL: "https://x"},
				Components: []supervisor.ComponentConfig{{Command: "/bin/x", Port: 8080}},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("name is required")))
		})

		It("requires every component to have a command", func() {
			cfg := supervisor.Config{
				Remote:     supervisor.RemoteConfig{BaseURL: "https://x"},
				Components: []supervisor.ComponentConfig{{Name: "x", Port: 8080}},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("command is required")))
		})

		It("requires every component to have a port", func() {
			cfg := supervisor.Config{
				Remote:     supervisor.RemoteConfig{BaseURL: "https://x"},
				Components: []supervisor.ComponentConfig{{Name: "x", Command: "/bin/x"}},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("port is required")))
		})

		It("rejects a port outside 1..65535", func() {
			cfg := supervisor.Config{
				Remote:     supervisor.RemoteConfig{BaseURL: "https://x"},
				Components: []supervisor.ComponentConfig{{Name: "x", Port: 70000, Command: "/bin/x"}},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("port is required")))
		})

		It("rejects duplicate component names", func() {
			cfg := supervisor.Config{
				Remote: supervisor.RemoteConfig{BaseURL: "https://x"},
				Components: []supervisor.ComponentConfig{
					{Name: "x", Port: 8080, Command: "/bin/x"},
					{Name: "x", Port: 8081, Command: "/bin/x2"},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("duplicate name")))
		})

		It("rejects two components sharing the same port", func() {
			cfg := supervisor.Config{
				Remote: supervisor.RemoteConfig{BaseURL: "https://x"},
				Components: []supervisor.ComponentConfig{
					{Name: "a", Port: 8080, Command: "/bin/a"},
					{Name: "b", Port: 8080, Command: "/bin/b"},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("port 8080 already used")))
		})

		It("requires a remote base_url for any configured component", func() {
			cfg := supervisor.Config{
				Components: []supervisor.ComponentConfig{
					{Name: "x", Port: 8080, Command: "/bin/x"},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("remote.base_url")))
		})

		It("accepts a config with no components", func() {
			cfg := supervisor.Config{}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(Succeed())
		})
	})

	Describe("LoadConfig", func() {
		It("parses the embedded default YAML", func() {
			dir := GinkgoT().TempDir()
			path := filepath.Join(dir, "supervisor.yml")
			Expect(os.WriteFile(path, []byte(supervisor.DefaultConfigYAML), 0644)).To(Succeed())

			cfg, err := supervisor.LoadConfig(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.StateDir).To(Equal("/var/lib/go-run"))
			Expect(cfg.StabilityTime).To(Equal(5 * time.Minute))
			Expect(cfg.Components).To(BeEmpty())
		})

		It("returns a useful error when the file is missing", func() {
			_, err := supervisor.LoadConfig("/nope/does/not/exist.yml")
			Expect(err).To(MatchError(ContainSubstring("read config")))
		})
	})
})
