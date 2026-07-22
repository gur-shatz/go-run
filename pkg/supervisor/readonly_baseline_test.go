package supervisor_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/pkg/supervisor"
)

// Specs in this file cover two things: the no-factory regression baseline (a
// component without factory config behaves exactly as it always has), and
// limp mode — startup and lifecycle against an unwritable state dir. Before
// limp mode, an unwritable state dir was fatal (supervisor.New errored, and a
// populated read-only state dir failed every launch on the kill.sock bind).

func mkTopCfg(stateDir string) supervisor.Config {
	cfg := supervisor.Config{
		StateDir:               stateDir,
		StabilityTime:          50 * time.Millisecond,
		CrashWindow:            500 * time.Millisecond,
		CrashThreshold:         2,
		ExecFailThreshold:      2,
		KillGracePeriod:        200 * time.Millisecond,
		VersionFolderRetention: 2,
	}
	cfg.ApplyDefaults()
	return cfg
}

// makeReadOnly chmods the given directories to 0555 and registers a cleanup
// that restores 0755 so TempDir removal succeeds.
func makeReadOnly(dirs ...string) {
	DeferCleanup(func() {
		for _, d := range dirs {
			_ = os.Chmod(d, 0755)
		}
	})
	for _, d := range dirs {
		Expect(os.Chmod(d, 0555)).To(Succeed())
	}
}

func skipIfRoot() {
	if os.Geteuid() == 0 {
		Skip("directory permissions do not restrict root")
	}
}

var _ = Describe("Baseline: component without factory config", func() {
	It("yields no target and marks warn when updates are disabled and current.txt is empty", func() {
		stateDir := GinkgoT().TempDir()
		paths := supervisor.NewPaths(stateDir).Component("api")
		Expect(paths.EnsureDirs()).To(Succeed())

		cfg := supervisor.ComponentConfig{
			Name:    "api",
			Command: "/bin/sh ./run.sh",
			Remote:  supervisor.RemoteConfig{PollingInterval: 100 * time.Millisecond},
		}
		topCfg := mkTopCfg(stateDir)
		topCfg.Components = []supervisor.ComponentConfig{cfg}
		install := &supervisor.Installer{Remote: supervisor.NewRemoteClient("")}
		bundle := supervisor.NewStatekitBundleForTest(topCfg)
		comp := supervisor.NewComponent(cfg, paths, install, topCfg, nil, bundle, log.New("[test]", false))

		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(BeEmpty())
		Expect(comp.Snapshot().Status).To(Equal("warn"))
		Expect(comp.Snapshot().StatusReason).To(ContainSubstring("current.txt is empty"))
	})

	It("keeps retrying a rejected crash-looping version when there is no stable to demote to", func() {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		origin := newFakeOrigin()
		server := httptest.NewServer(origin.serve())
		defer server.Close()

		stateDir := GinkgoT().TempDir()
		paths := supervisor.NewPaths(stateDir).Component("api")
		Expect(paths.EnsureDirs()).To(Succeed())

		client := supervisor.NewRemoteClient("")
		client.SetPlatform(runtimeGOOS(), runtimeGOARCH())
		install := &supervisor.Installer{Remote: client, PublicKey: pub}

		publishCrasher(origin, priv, "bad")

		cfg := supervisor.ComponentConfig{
			Name:    "api",
			Port:    freeTCPPort(),
			Command: "/bin/sh ./run.sh",
			Remote: supervisor.RemoteConfig{
				BaseURL:         server.URL,
				Target:          "required.txt",
				PollingInterval: 100 * time.Millisecond,
			},
		}
		topCfg := mkTopCfg(stateDir)
		topCfg.Components = []supervisor.ComponentConfig{cfg}
		bundle := supervisor.NewStatekitBundleForTest(topCfg)
		comp := supervisor.NewComponent(cfg, paths, install, topCfg, nil, bundle, log.New("[test]", false))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = comp.Run(ctx); close(done) }()

		Eventually(func() bool {
			rejected, _ := paths.IsRejected("bad")
			return rejected
		}, 5*time.Second).Should(BeTrue())

		// No stable exists, so demote cannot regress: current keeps naming the
		// rejected version and the lifecycle retries it under backoff.
		current, _ := paths.ReadCurrent()
		Expect(current).To(Equal("bad"))
		stable, _ := paths.ReadStable()
		Expect(stable).To(BeEmpty())
		Eventually(func() string { return comp.Snapshot().Status }, 3*time.Second).Should(Equal("fail"))

		cancel()
		Eventually(done, 3*time.Second).Should(BeClosed())
	})
})

var _ = Describe("Limp mode: unwritable state dir", func() {
	// AcquireLock itself still fails on a read-only dir — the skip-with-warning
	// decision lives in cmd/supervisor/main.go, which treats only
	// ErrAlreadyRunning as fatal.
	It("AcquireLock fails outright when the state dir is read-only", func() {
		skipIfRoot()
		stateDir := GinkgoT().TempDir()
		makeReadOnly(stateDir)

		lock, err := supervisor.AcquireLock(supervisor.NewPaths(stateDir).SupervisorLock())
		Expect(err).To(HaveOccurred())
		Expect(err).NotTo(MatchError(supervisor.ErrAlreadyRunning))
		Expect(lock).To(BeNil())
	})

	It("starts the full supervisor from a read-only state dir and runs the factory version", func() {
		skipIfRoot()
		stateDir := GinkgoT().TempDir()
		factoryDir := filepath.Join(GinkgoT().TempDir(), "factory", "api")
		writeFactoryBundle(factoryDir)
		makeReadOnly(stateDir)

		cfg := mkTopCfg(stateDir)
		cfg.Supervisor.BindAddress = "127.0.0.1:0"
		cfg.Components = []supervisor.ComponentConfig{{
			Name:    "api",
			Port:    freeTCPPort(),
			Command: "/bin/sh ./run.sh",
			Remote:  supervisor.RemoteConfig{PollingInterval: 100 * time.Millisecond},
			Factory: &supervisor.FactoryConfig{Dir: factoryDir, Version: factoryVersion},
		}}

		sup, err := supervisor.New(cfg, supervisor.Options{})
		Expect(err).NotTo(HaveOccurred(), "an unwritable state dir must not abort startup")

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = sup.Run(ctx); close(done) }()

		Eventually(func() int {
			snap, _ := sup.ComponentSnapshot("api")
			return snap.ChildPID
		}, 5*time.Second).ShouldNot(BeZero())
		snap, ok := sup.ComponentSnapshot("api")
		Expect(ok).To(BeTrue())
		Expect(snap.Current).To(Equal(factoryVersion))

		// Nothing was persisted: the read-only state dir holds no component dir.
		_, statErr := os.Stat(filepath.Join(stateDir, "api", "current.txt"))
		Expect(os.IsNotExist(statErr)).To(BeTrue())

		cancel()
		Eventually(done, 5*time.Second).Should(BeClosed())
	})

	It("starts the supervisor even when the state dir cannot be created at all", func() {
		skipIfRoot()
		parent := GinkgoT().TempDir()
		factoryDir := filepath.Join(GinkgoT().TempDir(), "factory", "api")
		writeFactoryBundle(factoryDir)
		makeReadOnly(parent)

		cfg := mkTopCfg(filepath.Join(parent, "state"))
		cfg.Supervisor.BindAddress = "127.0.0.1:0"
		cfg.Components = []supervisor.ComponentConfig{{
			Name:    "api",
			Port:    freeTCPPort(),
			Command: "/bin/sh ./run.sh",
			Remote:  supervisor.RemoteConfig{PollingInterval: 100 * time.Millisecond},
			Factory: &supervisor.FactoryConfig{Dir: factoryDir, Version: factoryVersion},
		}}

		_, err := supervisor.New(cfg, supervisor.Options{})
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("component lifecycle with in-memory state", func() {
		var (
			stateDir   string
			factoryDir string
			paths      supervisor.ComponentPaths
		)

		BeforeEach(func() {
			skipIfRoot()
			stateDir = GinkgoT().TempDir()
			factoryDir = filepath.Join(GinkgoT().TempDir(), "factory", "api")
			writeFactoryBundle(factoryDir)
			paths = supervisor.NewPaths(stateDir).Component("api")
			Expect(paths.EnsureDirs()).To(Succeed())
		})

		populateVersion := func(version, script string) {
			versionDir := paths.VersionDir(version)
			Expect(os.MkdirAll(versionDir, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(versionDir, "run.sh"), []byte(script), 0755)).To(Succeed())
			Expect(paths.WriteCurrent(version)).To(Succeed())
		}

		lockDownStateDir := func(version string) {
			makeReadOnly(paths.VersionDir(version), paths.Versions(), paths.Root, stateDir)
		}

		startLimpComponent := func(remote supervisor.RemoteConfig, install *supervisor.Installer) (*supervisor.Component, context.CancelFunc, chan struct{}) {
			cfg := supervisor.ComponentConfig{
				Name:    "api",
				Port:    freeTCPPort(),
				Command: "/bin/sh ./run.sh",
				Remote:  remote,
				Factory: &supervisor.FactoryConfig{Dir: factoryDir, Version: factoryVersion},
			}
			topCfg := mkTopCfg(stateDir)
			topCfg.Components = []supervisor.ComponentConfig{cfg}
			bundle := supervisor.NewStatekitBundleForTest(topCfg)
			comp := supervisor.NewComponent(cfg, paths.WithMemoryState(), install, topCfg, nil, bundle, log.New("[test]", false))
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { _ = comp.Run(ctx); close(done) }()
			return comp, cancel, done
		}

		It("honors a valid current.txt in a populated read-only state dir", func() {
			sleeper := "#!/bin/sh\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n"
			populateVersion("1.0.0", sleeper)
			lockDownStateDir("1.0.0")

			comp, cancel, done := startLimpComponent(
				supervisor.RemoteConfig{PollingInterval: 100 * time.Millisecond},
				&supervisor.Installer{Remote: supervisor.NewRemoteClient("")})
			defer func() {
				cancel()
				Eventually(done, 5*time.Second).Should(BeClosed())
			}()

			// The populated read-only state keeps running what it last had —
			// no kill.sock and no on-disk log, but the child runs.
			Eventually(func() int { return comp.Snapshot().ChildPID }, 5*time.Second).ShouldNot(BeZero())
			Expect(comp.Snapshot().Current).To(Equal("1.0.0"))
			_, statErr := os.Stat(paths.KillSock())
			Expect(os.IsNotExist(statErr)).To(BeTrue())
		})

		It("demotes a crash-looping current to factory in memory", func() {
			populateVersion("1.0.0", "#!/bin/sh\nexit 1\n")
			lockDownStateDir("1.0.0")

			comp, cancel, done := startLimpComponent(
				supervisor.RemoteConfig{PollingInterval: 100 * time.Millisecond},
				&supervisor.Installer{Remote: supervisor.NewRemoteClient("")})
			defer func() {
				cancel()
				Eventually(done, 5*time.Second).Should(BeClosed())
			}()

			Eventually(func() string { return comp.Snapshot().Current }, 5*time.Second).Should(Equal(factoryVersion))
			Eventually(func() int { return comp.Snapshot().ChildPID }, 5*time.Second).ShouldNot(BeZero())

			// The demotion happened in memory only: disk still names the
			// crasher (and could not have been written anyway).
			data, err := os.ReadFile(filepath.Join(paths.Root, "current.txt"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring("1.0.0"))
		})

		It("keeps the pointer poll live but fetches no image while unwritable", func() {
			pub, priv, err := ed25519.GenerateKey(rand.Reader)
			Expect(err).NotTo(HaveOccurred())
			origin := newFakeOrigin()
			server := httptest.NewServer(origin.serve())
			defer server.Close()

			client := supervisor.NewRemoteClient("")
			client.SetPlatform(runtimeGOOS(), runtimeGOARCH())

			publishWithBinary(origin, priv, "9.0.0")

			lockDownStateDir("") // no populated version; factory boots
			comp, cancel, done := startLimpComponent(
				supervisor.RemoteConfig{
					BaseURL:         server.URL,
					Target:          "required.txt",
					PollingInterval: 100 * time.Millisecond,
				},
				&supervisor.Installer{Remote: client, PublicKey: pub})
			defer func() {
				cancel()
				Eventually(done, 5*time.Second).Should(BeClosed())
			}()

			Eventually(func() int { return comp.Snapshot().ChildPID }, 5*time.Second).ShouldNot(BeZero())
			Expect(comp.Snapshot().Current).To(Equal(factoryVersion))

			// Health reports the pending update and the reason it cannot land.
			Eventually(func() string { return comp.Snapshot().UpdateReason }, 3*time.Second).
				Should(SatisfyAll(ContainSubstring("9.0.0"), ContainSubstring("not writable")))

			for _, path := range origin.recordedRequests() {
				Expect(path).NotTo(ContainSubstring("/images/"), "no image fetch may occur in limp mode")
			}
		})
	})
})
