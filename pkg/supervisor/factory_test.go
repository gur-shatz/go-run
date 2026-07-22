package supervisor_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/pkg/supervisor"
)

const factoryVersion = "260722_113000_factory"

func (this *fakeOrigin) recordedRequests() []string {
	this.mu.Lock()
	defer this.mu.Unlock()
	return append([]string(nil), this.requests...)
}

// writeFactoryBundle creates an image-provided factory dir (outside the state
// dir) holding a run.sh that sleeps until SIGTERM.
func writeFactoryBundle(dir string) {
	Expect(os.MkdirAll(dir, 0755)).To(Succeed())
	script := "#!/bin/sh\necho factory started >&2\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n"
	Expect(os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0755)).To(Succeed())
}

var _ = Describe("Factory version", func() {
	var (
		stateDir   string
		factoryDir string
		paths      supervisor.ComponentPaths
		topCfg     supervisor.Config
	)

	BeforeEach(func() {
		stateDir = GinkgoT().TempDir()
		factoryDir = filepath.Join(GinkgoT().TempDir(), "factory", "connector")
		writeFactoryBundle(factoryDir)

		paths = supervisor.NewPaths(stateDir).Component("api")
		Expect(paths.EnsureDirs()).To(Succeed())

		topCfg = mkTopCfg(stateDir)
	})

	mkFactoryCfg := func(remote supervisor.RemoteConfig) supervisor.ComponentConfig {
		return supervisor.ComponentConfig{
			Name:    "api",
			Port:    freeTCPPort(),
			Command: "/bin/sh ./run.sh",
			Remote:  remote,
			Factory: &supervisor.FactoryConfig{Dir: factoryDir, Version: factoryVersion},
		}
	}

	startComponent := func(cfg supervisor.ComponentConfig, install *supervisor.Installer) (*supervisor.Component, context.CancelFunc, chan struct{}) {
		topCfg.Components = []supervisor.ComponentConfig{cfg}
		bundle := supervisor.NewStatekitBundleForTest(topCfg)
		comp := supervisor.NewComponent(cfg, paths, install, topCfg, nil, bundle, log.New("[test]", false))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = comp.Run(ctx); close(done) }()
		return comp, cancel, done
	}

	stopComponent := func(cancel context.CancelFunc, done chan struct{}) {
		cancel()
		Eventually(done, 3*time.Second).Should(BeClosed())
	}

	noRemoteInstaller := func() *supervisor.Installer {
		return &supervisor.Installer{Remote: supervisor.NewRemoteClient("")}
	}

	It("launches from factory on an empty state dir and commits current.txt (updates disabled)", func() {
		cfg := mkFactoryCfg(supervisor.RemoteConfig{PollingInterval: 100 * time.Millisecond})
		comp, cancel, done := startComponent(cfg, noRemoteInstaller())
		defer stopComponent(cancel, done)

		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())
		Expect(comp.Snapshot().Current).To(Equal(factoryVersion))
		current, err := paths.ReadCurrent()
		Expect(err).NotTo(HaveOccurred())
		Expect(current).To(Equal(factoryVersion))
	})

	It("boots from factory when the origin is unreachable, then adopts the remote version once it resolves", func() {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		origin := newFakeOrigin()
		server := httptest.NewServer(origin.serve())
		defer server.Close()

		client := supervisor.NewRemoteClient("")
		client.SetPlatform(runtimeGOOS(), runtimeGOARCH())
		install := &supervisor.Installer{Remote: client, PublicKey: pub}

		// No required.txt published yet: every poll fails.
		cfg := mkFactoryCfg(supervisor.RemoteConfig{
			BaseURL:         server.URL,
			Target:          "required.txt",
			PollingInterval: 100 * time.Millisecond,
		})
		comp, cancel, done := startComponent(cfg, install)
		defer stopComponent(cancel, done)

		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())
		Expect(comp.Snapshot().Current).To(Equal(factoryVersion))

		// Origin comes up naming a new version: normal download/switch.
		publishWithBinary(origin, priv, "2.0.0")
		Eventually(func() string { return comp.Snapshot().Current }, 5*time.Second).Should(Equal("2.0.0"))
		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())
	})

	It("fetches no image when required.txt names the factory version", func() {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		origin := newFakeOrigin()
		origin.files["/api/versions/required.txt"] = factoryVersion
		server := httptest.NewServer(origin.serve())
		defer server.Close()

		client := supervisor.NewRemoteClient("")
		client.SetPlatform(runtimeGOOS(), runtimeGOARCH())
		install := &supervisor.Installer{Remote: client, PublicKey: pub}

		cfg := mkFactoryCfg(supervisor.RemoteConfig{
			BaseURL:         server.URL,
			Target:          "required.txt",
			PollingInterval: 100 * time.Millisecond,
		})
		comp, cancel, done := startComponent(cfg, install)
		defer stopComponent(cancel, done)

		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())
		Expect(comp.Snapshot().Current).To(Equal(factoryVersion))

		for _, path := range origin.recordedRequests() {
			Expect(path).NotTo(ContainSubstring("/images/"), "no image fetch may occur for the factory version")
		}
	})

	It("demotes a crash-looping version to factory when no stable exists", func() {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		origin := newFakeOrigin()
		server := httptest.NewServer(origin.serve())
		defer server.Close()

		client := supervisor.NewRemoteClient("")
		client.SetPlatform(runtimeGOOS(), runtimeGOARCH())
		install := &supervisor.Installer{Remote: client, PublicKey: pub}

		publishCrasher(origin, priv, "bad")

		cfg := mkFactoryCfg(supervisor.RemoteConfig{
			BaseURL:         server.URL,
			Target:          "required.txt",
			PollingInterval: 100 * time.Millisecond,
		})
		comp, cancel, done := startComponent(cfg, install)
		defer stopComponent(cancel, done)

		Eventually(func() bool {
			rejected, _ := paths.IsRejected("bad")
			return rejected
		}, 5*time.Second).Should(BeTrue())
		Eventually(func() string {
			c, _ := paths.ReadCurrent()
			return c
		}, 5*time.Second).Should(Equal(factoryVersion))
		Eventually(func() int { return comp.Snapshot().ChildPID }, 5*time.Second).ShouldNot(BeZero())
	})

	It("prefers a usable downloaded version over a rejected factory", func() {
		// A downloaded version sits extracted on disk, current.txt names it,
		// updates are disabled, and the factory is rejected.
		versionDir := paths.VersionDir("3.0.0")
		Expect(os.MkdirAll(versionDir, 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(versionDir, "run.sh"), []byte("#!/bin/sh\nsleep 1\n"), 0755)).To(Succeed())
		Expect(paths.WriteCurrent("3.0.0")).To(Succeed())
		Expect(paths.AppendReject(factoryVersion)).To(Succeed())

		cfg := mkFactoryCfg(supervisor.RemoteConfig{PollingInterval: 100 * time.Millisecond})
		topCfg.Components = []supervisor.ComponentConfig{cfg}
		comp := supervisor.NewComponent(cfg, paths, noRemoteInstaller(), topCfg, nil, nil, log.New("[test]", false))

		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("3.0.0"))
	})

	It("keeps retrying a rejected factory when nothing else is usable", func() {
		Expect(paths.WriteCurrent(factoryVersion)).To(Succeed())
		Expect(paths.AppendReject(factoryVersion)).To(Succeed())

		cfg := mkFactoryCfg(supervisor.RemoteConfig{PollingInterval: 100 * time.Millisecond})
		topCfg.Components = []supervisor.ComponentConfig{cfg}
		comp := supervisor.NewComponent(cfg, paths, noRemoteInstaller(), topCfg, nil, nil, log.New("[test]", false))

		// Target equals current, so the rejection filter does not hold it back:
		// the lifecycle keeps retrying the factory under backoff, as any
		// rejected current version is retried today.
		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal(factoryVersion))
	})

	It("pins to the factory via a forced version override", func() {
		override := supervisor.ForcedOverride{Kind: supervisor.ForcedKindVersion, Version: factoryVersion}
		getOverride := func() supervisor.ForcedOverride { return override }

		cfg := mkFactoryCfg(supervisor.RemoteConfig{PollingInterval: 100 * time.Millisecond})
		topCfg.Components = []supervisor.ComponentConfig{cfg}
		comp := supervisor.NewComponent(cfg, paths, noRemoteInstaller(), topCfg, getOverride, nil, log.New("[test]", false))

		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal(factoryVersion))
		Expect(comp.PrepareVersion(context.Background(), factoryVersion)).To(Succeed())
	})

	It("leaves the factory dir untouched by GC with zero retention", func() {
		factoryPaths := paths.WithFactory(factoryDir, factoryVersion)
		for _, orphan := range []string{"old-1", "old-2"} {
			dir := filepath.Join(factoryPaths.Versions(), orphan)
			Expect(os.MkdirAll(dir, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "x"), []byte("x"), 0644)).To(Succeed())
		}
		Expect(factoryPaths.WriteCurrent(factoryVersion)).To(Succeed())

		result, err := supervisor.CleanOrphanVersions(factoryPaths, 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Deleted).To(ConsistOf("old-1", "old-2"))

		entries, err := os.ReadDir(factoryDir)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).NotTo(BeEmpty(), "factory dir must survive GC")
	})

	Describe("config validation", func() {
		mkValidComponent := func() supervisor.ComponentConfig {
			return supervisor.ComponentConfig{
				Name:    "api",
				Port:    8080,
				Command: "/bin/true",
				Factory: &supervisor.FactoryConfig{Dir: factoryDir, Version: factoryVersion},
			}
		}

		validate := func(mutate func(*supervisor.ComponentConfig)) error {
			c := mkValidComponent()
			mutate(&c)
			cfg := supervisor.Config{StateDir: stateDir, Components: []supervisor.ComponentConfig{c}}
			cfg.ApplyDefaults()
			return cfg.Validate()
		}

		It("accepts a populated factory dir", func() {
			Expect(validate(func(c *supervisor.ComponentConfig) {})).To(Succeed())
		})

		It("requires dir and version together", func() {
			err := validate(func(c *supervisor.ComponentConfig) { c.Factory.Version = "" })
			Expect(err).To(MatchError(ContainSubstring("dir and version are both required")))
			err = validate(func(c *supervisor.ComponentConfig) { c.Factory.Dir = "" })
			Expect(err).To(MatchError(ContainSubstring("dir and version are both required")))
		})

		It("rejects the local snapshot name as a factory version", func() {
			err := validate(func(c *supervisor.ComponentConfig) { c.Factory.Version = "." })
			Expect(err).To(MatchError(ContainSubstring(`must not be "."`)))
		})

		It("rejects a missing factory dir", func() {
			err := validate(func(c *supervisor.ComponentConfig) { c.Factory.Dir = filepath.Join(stateDir, "nope") })
			Expect(err).To(MatchError(ContainSubstring("not readable")))
		})

		It("rejects an empty factory dir", func() {
			empty := GinkgoT().TempDir()
			err := validate(func(c *supervisor.ComponentConfig) { c.Factory.Dir = empty })
			Expect(err).To(MatchError(ContainSubstring("is empty")))
		})
	})

	It("resolves a relative factory dir against the config file directory", func() {
		configDir := GinkgoT().TempDir()
		bundleDir := filepath.Join(configDir, "factory", "api")
		writeFactoryBundle(bundleDir)

		configYAML := strings.Join([]string{
			"state_dir: " + stateDir,
			"components:",
			"  - name: api",
			"    port: 8080",
			"    command: '/bin/sh ./run.sh'",
			"    factory:",
			"      dir: factory/api",
			"      version: " + factoryVersion,
		}, "\n")
		configPath := filepath.Join(configDir, "supervisor.yml")
		Expect(os.WriteFile(configPath, []byte(configYAML), 0644)).To(Succeed())

		cfg, err := supervisor.LoadConfig(configPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Components[0].Factory.Dir).To(Equal(bundleDir))
	})
})
