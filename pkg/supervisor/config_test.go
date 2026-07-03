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
			Expect(cfg.Supervisor.Favicon.Name).To(Equal("GR"))
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
			Expect(cfg.Components[0].Remote.Enabled).To(BeTrue())
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
			Expect(cfg.Components[0].Remote.Enabled).To(BeTrue())
			Expect(cfg.Components[0].Remote.PollingInterval).To(Equal(30 * time.Second))
		})

		It("lets updates.enabled false disable remote polling even when remote fields are present", func() {
			disabled := false
			cfg := supervisor.Config{
				Updates: supervisor.UpdatesConfig{Enabled: &disabled},
				Remote:  supervisor.RemoteConfig{BaseURL: "https://updates.example.com"},
				Components: []supervisor.ComponentConfig{
					{Name: "x", Port: 8080, Command: "/bin/x"},
				},
			}
			cfg.ApplyDefaults()

			Expect(cfg.Components[0].Remote.BaseURL).To(Equal("https://updates.example.com"))
			Expect(cfg.Components[0].Remote.Enabled).To(BeFalse())
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

		It("fills only reachability URL defaults on external components", func() {
			cfg := supervisor.Config{
				ExternalComponents: []supervisor.ExternalComponentConfig{{Name: "docs", URL: "http://docs.internal:8080"}},
			}
			cfg.ApplyDefaults()
			Expect(cfg.ExternalComponents[0].URLs.Healthz).To(Equal("/healthz"))
			Expect(cfg.ExternalComponents[0].URLs.Readyz).To(Equal("/readyz"))
			Expect(cfg.ExternalComponents[0].URLs.State).To(BeEmpty())
			Expect(cfg.ExternalComponents[0].URLs.Metrics).To(BeEmpty())
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

		It("allows a component with no remote base_url for local snapshot mode", func() {
			cfg := supervisor.Config{
				Components: []supervisor.ComponentConfig{
					{Name: "x", Port: 8080, Command: "/bin/x"},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(Succeed())
		})

		It("requires a remote base_url when updates are explicitly enabled", func() {
			enabled := true
			cfg := supervisor.Config{
				Updates: supervisor.UpdatesConfig{Enabled: &enabled},
				Components: []supervisor.ComponentConfig{
					{Name: "x", Port: 8080, Command: "/bin/x"},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("updates are enabled")))
		})

		It("accepts a config with no components", func() {
			cfg := supervisor.Config{}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(Succeed())
		})

		It("accepts a config with only external components", func() {
			cfg := supervisor.Config{
				ExternalComponents: []supervisor.ExternalComponentConfig{{Name: "docs", URL: "http://docs.internal:8080"}},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(Succeed())
		})

		It("accepts proxy_urls on managed and external components", func() {
			cfg := supervisor.Config{
				Components: []supervisor.ComponentConfig{
					{Name: "api", Port: 8080, Command: "/bin/api", ProxyURLs: map[string]string{"admin": ":9090/backoffice"}},
				},
				ExternalComponents: []supervisor.ExternalComponentConfig{
					{Name: "docs", URL: "http://docs.internal:8080", ProxyURLs: map[string]string{"app": "https://docs.example.com/app"}},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(Succeed())
		})

		It("rejects proxy_urls with invalid keys or empty targets", func() {
			cfg := supervisor.Config{
				Components: []supervisor.ComponentConfig{
					{Name: "api", Port: 8080, Command: "/bin/api", ProxyURLs: map[string]string{"bad/key": ":9090/backoffice"}},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("must not contain /")))

			cfg.Components[0].ProxyURLs = map[string]string{"admin": ""}
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("admin is required")))
		})

		It("rejects duplicate names across managed and external components", func() {
			cfg := supervisor.Config{
				Components:         []supervisor.ComponentConfig{{Name: "docs", Port: 8080, Command: "/bin/docs"}},
				ExternalComponents: []supervisor.ExternalComponentConfig{{Name: "docs", URL: "http://docs.internal:8080"}},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("duplicate name")))
		})

		It("requires external component URLs to be absolute HTTP URLs", func() {
			cfg := supervisor.Config{
				ExternalComponents: []supervisor.ExternalComponentConfig{{Name: "docs", URL: "docs.internal:8080"}},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("http:// or https://")))
		})

		It("rejects basic auth enabled without a username or password", func() {
			cfg := supervisor.Config{}
			cfg.Supervisor.BasicAuth = supervisor.BasicAuthConfig{Enabled: true, Username: "op"}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("username and password are required")))
		})

		It("accepts basic auth with both credentials", func() {
			cfg := supervisor.Config{}
			cfg.Supervisor.BasicAuth = supervisor.BasicAuthConfig{Enabled: true, Username: "op", Password: "s3cret"}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(Succeed())
		})

		It("rejects favicon names longer than two characters", func() {
			cfg := supervisor.Config{}
			cfg.Supervisor.Favicon.Name = "supervisor"
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("supervisor.favicon.name")))
		})

		It("rejects setting both memory.share and memory.hardlimit", func() {
			cfg := supervisor.Config{
				Components: []supervisor.ComponentConfig{
					{Name: "leaker", Port: 8080, Command: "/bin/l",
						Memory: &supervisor.ComponentMemoryConfig{Share: 0.4, HardLimit: 50 << 20}},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("mutually exclusive")))
		})

		It("rejects a softlimit greater than the hardlimit", func() {
			cfg := supervisor.Config{
				Components: []supervisor.ComponentConfig{
					{Name: "leaker", Port: 8080, Command: "/bin/l",
						Memory: &supervisor.ComponentMemoryConfig{SoftLimit: 60 << 20, HardLimit: 40 << 20}},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("softlimit")))
		})

		It("accepts an explicit soft + hard limit pair", func() {
			cfg := supervisor.Config{
				Components: []supervisor.ComponentConfig{
					{Name: "leaker", Port: 8080, Command: "/bin/l",
						Memory: &supervisor.ComponentMemoryConfig{SoftLimit: 30 << 20, HardLimit: 40 << 20}},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(Succeed())
		})

		It("accepts a component with only a hardlimit (no share, no global limit)", func() {
			cfg := supervisor.Config{
				Components: []supervisor.ComponentConfig{
					{Name: "leaker", Port: 8080, Command: "/bin/l",
						Memory: &supervisor.ComponentMemoryConfig{HardLimit: 50 << 20}},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Validate()).To(Succeed())
		})

		It("rejects a pod_pressure_high outside [0,1]", func() {
			cfg := supervisor.Config{
				Memory: &supervisor.MemoryConfig{PodPressureHigh: 1.5},
			}
			cfg.ApplyDefaults()
			cfg.Memory.PodPressureHigh = 1.5 // re-assert after defaults
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("pod_pressure_high")))
		})
	})

	Describe("memory defaults", func() {
		It("fills the global enforcement defaults", func() {
			cfg := supervisor.Config{
				Components: []supervisor.ComponentConfig{
					{Name: "gateway", Port: 8080, Command: "/bin/g",
						Memory: &supervisor.ComponentMemoryConfig{Share: 0.4}},
				},
			}
			cfg.ApplyDefaults()
			Expect(cfg.Memory.SustainedFor).To(Equal(60 * time.Second))
			Expect(cfg.Memory.PodPressureHigh).To(BeNumerically("~", 0.90, 1e-9))
			Expect(cfg.Memory.PodPressurePSI).To(BeNumerically("~", 0.10, 1e-9))
			Expect(cfg.Memory.PSSInterval).To(Equal(60 * time.Second))
		})

		It("defaults monitor_only to false (the component is enforced)", func() {
			cm := &supervisor.ComponentMemoryConfig{HardLimit: 40 << 20}
			Expect(cm.IsMonitorOnly()).To(BeFalse())
		})

		It("honors an explicit monitor_only: true (tracked but never killed)", func() {
			on := true
			cm := &supervisor.ComponentMemoryConfig{HardLimit: 40 << 20, MonitorOnly: &on}
			Expect(cm.IsMonitorOnly()).To(BeTrue())
		})

		It("defaults supervisor enforcement (memory.enforce) to on, independent of cgroups", func() {
			m := &supervisor.MemoryConfig{} // nil Enforce
			Expect(m.IsEnforcing()).To(BeTrue())
		})

		It("honors an explicit enforce: false (tracking + state, no supervisor actions)", func() {
			off := false
			m := &supervisor.MemoryConfig{Enforce: &off}
			Expect(m.IsEnforcing()).To(BeFalse())
		})

		It("reports not enforcing when the whole subsystem is disabled", func() {
			disabled, on := false, true
			m := &supervisor.MemoryConfig{Enabled: &disabled, Enforce: &on}
			Expect(m.IsEnforcing()).To(BeFalse())
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

	Describe("LoadConfig: template expansion", func() {
		var path string

		writeYAML := func(body string) {
			dir := GinkgoT().TempDir()
			path = filepath.Join(dir, "supervisor.yml")
			Expect(os.WriteFile(path, []byte(body), 0644)).To(Succeed())
		}

		It("resolves vars referencing each other (iterative)", func() {
			writeYAML(`
state_dir: ./state
vars:
  REGION: us-east
  UPSTREAM: '{{ .REGION }}.example.com'
remote:
  base_url: https://x
`)
			cfg, err := supervisor.LoadConfig(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Vars["UPSTREAM"]).To(Equal("us-east.example.com"))
		})

		It("resolves vars from environment via {{ env }}", func() {
			os.Setenv("SUP_TEST_ENV", "from-env")
			DeferCleanup(func() { os.Unsetenv("SUP_TEST_ENV") })

			writeYAML(`
state_dir: ./state
vars:
  WHO: '{{ env "SUP_TEST_ENV" }}'
remote:
  base_url: https://x
`)
			cfg, err := supervisor.LoadConfig(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Vars["WHO"]).To(Equal("from-env"))
		})

		It("expands templates in component fields (port computed from a var)", func() {
			// [[ ]] is the unquoted-friendly delimiter — runctl uses it for
			// exactly this case so the substituted int survives YAML round-trip.
			writeYAML(`
state_dir: ./state
vars:
  BASE_PORT: 18000
remote:
  base_url: https://x
components:
  - name: hello
    port: [[ add .BASE_PORT 90 | int ]]
    command: "${VERSION_DIR}/bin/hello"
`)
			cfg, err := supervisor.LoadConfig(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Components[0].Port).To(Equal(18090))
		})

		It("leaves ${VAR} command syntax untouched (those expand at launch)", func() {
			writeYAML(`
state_dir: ./state
remote:
  base_url: https://x
components:
  - name: hello
    port: 18090
    command: "${VERSION_DIR}/bin/hello --port=${MONITOR_PORT}"
`)
			cfg, err := supervisor.LoadConfig(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Components[0].Command).To(Equal("${VERSION_DIR}/bin/hello --port=${MONITOR_PORT}"))
		})

		It("parses a component overflow path", func() {
			writeYAML(`
state_dir: ./state
remote:
  base_url: https://x
components:
  - name: hello
    port: 18090
    command: "./bin/hello"
    memory:
      overflow-path: "/pprof/dump"
`)
			cfg, err := supervisor.LoadConfig(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Components[0].Memory.OverflowPath).To(Equal("/pprof/dump"))
		})

		It("rejects overflow paths that try to override the component base URL", func() {
			writeYAML(`
state_dir: ./state
remote:
  base_url: https://x
components:
  - name: hello
    port: 18090
    command: "./bin/hello"
    memory:
      overflow-path: "http://127.0.0.1:18091/pprof/dump"
`)
			_, err := supervisor.LoadConfig(path)
			Expect(err).To(MatchError(ContainSubstring("overflow-path")))
			Expect(err).To(MatchError(ContainSubstring("absolute URL")))
		})

		It("rejects overflow paths that try to override the component port", func() {
			writeYAML(`
state_dir: ./state
remote:
  base_url: https://x
components:
  - name: hello
    port: 18090
    command: "./bin/hello"
    memory:
      overflow-path: ":18091/pprof/dump"
`)
			_, err := supervisor.LoadConfig(path)
			Expect(err).To(MatchError(ContainSubstring("overflow-path")))
			Expect(err).To(MatchError(ContainSubstring("port override")))
		})

		It("returns an error for an undefined template variable", func() {
			writeYAML(`
state_dir: ./state
remote:
  base_url: '{{ required "MISSING is required" .MISSING }}'
`)
			_, err := supervisor.LoadConfig(path)
			Expect(err).To(MatchError(ContainSubstring("MISSING is required")))
		})
	})
})
