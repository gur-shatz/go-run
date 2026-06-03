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

// publishWithBinary creates a real /bin/sh-driven version archive that the
// component lifecycle can actually exec. The archive contains a single
// executable that sleeps until SIGTERM (or until the kill socket fires).
func publishWithBinary(origin *fakeOrigin, priv ed25519.PrivateKey, version string) {
	body := "#!/bin/sh\necho started >&2\ntrap 'echo exiting >&2; exit 0' TERM INT\nwhile :; do sleep 1; done\n"
	archive, sig := buildSignedImage(priv, map[string]string{"run.sh": body})
	origin.files["/api/versions/required.txt"] = version
	origin.files["/api/images/"+version+"_"+runtimeGOOS()+"_"+runtimeGOARCH()+".tar.gz"] = string(archive)
	origin.files["/api/images/"+version+"_"+runtimeGOOS()+"_"+runtimeGOARCH()+".tar.gz.sig"] = string(sig)
}

// publishCrasher publishes a script that exits immediately, exercising the
// fast-crash counter path.
func publishCrasher(origin *fakeOrigin, priv ed25519.PrivateKey, version string) {
	body := "#!/bin/sh\nexit 1\n"
	archive, sig := buildSignedImage(priv, map[string]string{"run.sh": body})
	origin.files["/api/versions/required.txt"] = version
	origin.files["/api/images/"+version+"_"+runtimeGOOS()+"_"+runtimeGOARCH()+".tar.gz"] = string(archive)
	origin.files["/api/images/"+version+"_"+runtimeGOOS()+"_"+runtimeGOARCH()+".tar.gz.sig"] = string(sig)
}

func runtimeGOOS() string {
	return goosForTest
}

func runtimeGOARCH() string {
	return goarchForTest
}

// runtime.GOOS / runtime.GOARCH are baked in below via a helper file (oslocal_test.go)
// to keep this file generic.

var _ = Describe("Component lifecycle", func() {
	var (
		origin   *fakeOrigin
		server   *httptest.Server
		stateDir string
		paths    supervisor.ComponentPaths
		pub      ed25519.PublicKey
		priv     ed25519.PrivateKey
		client   *supervisor.RemoteClient
		install  *supervisor.Installer
		topCfg   supervisor.Config
	)

	BeforeEach(func() {
		var err error
		pub, priv, err = ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())

		origin = newFakeOrigin()
		server = httptest.NewServer(origin.serve())

		stateDir = GinkgoT().TempDir()
		paths = supervisor.NewPaths(stateDir).Component("api")
		Expect(paths.EnsureDirs()).To(Succeed())

		client = supervisor.NewRemoteClient("")
		client.SetPlatform(runtimeGOOS(), runtimeGOARCH())
		install = &supervisor.Installer{Remote: client, PublicKey: pub}

		topCfg = supervisor.Config{
			StateDir:               stateDir,
			StabilityTime:          50 * time.Millisecond,
			CrashWindow:            500 * time.Millisecond,
			CrashThreshold:         2,
			ExecFailThreshold:      2,
			KillGracePeriod:        200 * time.Millisecond,
			VersionFolderRetention: 2,
		}
		topCfg.ApplyDefaults()
	})

	AfterEach(func() { server.Close() })

	mkComponentCfg := func() supervisor.ComponentConfig {
		return supervisor.ComponentConfig{
			Name:    "api",
			Port:    freeTCPPort(),
			Command: "/bin/sh ./run.sh",
			Remote: supervisor.RemoteConfig{
				BaseURL:         server.URL,
				Target:          "required.txt",
				PollingInterval: 100 * time.Millisecond,
			},
		}
	}

	It("installs, launches, and reports a running PID", func() {
		publishWithBinary(origin, priv, "1.0.0")

		comp := supervisor.NewComponent(mkComponentCfg(), paths, install, topCfg, nil, nil, log.New("[test]", false))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan struct{})
		go func() { _ = comp.Run(ctx); close(done) }()

		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())
		Expect(comp.Snapshot().ChildPID).NotTo(BeZero())
		Expect(comp.Snapshot().Current).To(Equal("1.0.0"))

		cancel()
		Eventually(done, 3*time.Second).Should(BeClosed())
	})

	It("supports manual stop, start, and restart controls", func() {
		publishWithBinary(origin, priv, "1.0.0")

		comp := supervisor.NewComponent(mkComponentCfg(), paths, install, topCfg, nil, nil, log.New("[test]", false))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan struct{})
		go func() { _ = comp.Run(ctx); close(done) }()

		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())
		firstPID := comp.Snapshot().ChildPID

		controlCtx, controlCancel := context.WithTimeout(context.Background(), 3*time.Second)
		Expect(comp.Stop(controlCtx)).To(Succeed())
		controlCancel()
		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).Should(BeZero())
		Consistently(func() int { return comp.Snapshot().ChildPID }, 250*time.Millisecond).Should(BeZero())

		controlCtx, controlCancel = context.WithTimeout(context.Background(), 3*time.Second)
		Expect(comp.Start(controlCtx)).To(Succeed())
		controlCancel()
		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())
		secondPID := comp.Snapshot().ChildPID
		Expect(secondPID).NotTo(Equal(firstPID))

		controlCtx, controlCancel = context.WithTimeout(context.Background(), 3*time.Second)
		Expect(comp.Restart(controlCtx)).To(Succeed())
		controlCancel()
		Eventually(func() int {
			pid := comp.Snapshot().ChildPID
			if pid == secondPID {
				return 0
			}
			return pid
		}, 3*time.Second).ShouldNot(BeZero())

		cancel()
		Eventually(done, 3*time.Second).Should(BeClosed())
	})

	It("rolls back to stable when current is crashing fast", func() {
		// Seed stable.
		publishWithBinary(origin, priv, "good")
		comp := supervisor.NewComponent(mkComponentCfg(), paths, install, topCfg, nil, nil, log.New("[test]", false))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = comp.Run(ctx); close(done) }()

		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())
		// Wait for promotion to stable (stability_time=50ms).
		Eventually(func() string {
			s, _ := paths.ReadStable()
			return s
		}, 3*time.Second).Should(Equal("good"))

		// Switch remote to a crasher.
		publishCrasher(origin, priv, "bad")
		// Wait for the supervisor to demote back to good after the bad version is rejected.
		Eventually(func() bool {
			rejected, _ := paths.IsRejected("bad")
			return rejected
		}, 5*time.Second).Should(BeTrue())
		Eventually(func() string {
			c, _ := paths.ReadCurrent()
			return c
		}, 5*time.Second).Should(Equal("good"))

		cancel()
		Eventually(done, 3*time.Second).Should(BeClosed())
	})

	It("respects a forced version override", func() {
		publishWithBinary(origin, priv, "1.0.0")
		publishWithBinary(origin, priv, "2.0.0")
		origin.files["/api/versions/required.txt"] = "1.0.0"

		override := supervisor.ForcedOverride{Kind: supervisor.ForcedKindVersion, Version: "2.0.0"}
		getOverride := func() supervisor.ForcedOverride { return override }

		comp := supervisor.NewComponent(mkComponentCfg(), paths, install, topCfg, getOverride, nil, log.New("[test]", false))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = comp.Run(ctx); close(done) }()

		Eventually(func() string { return comp.Snapshot().Current }, 3*time.Second).Should(Equal("2.0.0"))

		cancel()
		Eventually(done, 3*time.Second).Should(BeClosed())
	})

	It("creates the kill socket with 0600 permissions while running", func() {
		publishWithBinary(origin, priv, "1.0.0")
		comp := supervisor.NewComponent(mkComponentCfg(), paths, install, topCfg, nil, nil, log.New("[test]", false))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = comp.Run(ctx); close(done) }()

		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())

		info, err := os.Stat(filepath.Join(paths.Root, "kill.sock"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0600)))

		cancel()
		Eventually(done, 3*time.Second).Should(BeClosed())
	})

	It("runs local snapshot mode from current.txt = . without a remote", func() {
		Expect(os.WriteFile(filepath.Join(paths.Root, "manifest.yml"), []byte(`validate_templates:
  - config.yml
default_vars:
  GREETING: hello
`), 0644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(paths.Root, "config.yml.tmpl"), []byte(`greeting: "{{ .GREETING }}"`), 0644)).To(Succeed())
		Expect(paths.WriteCurrent(".")).To(Succeed())

		cfg := mkComponentCfg()
		cfg.Command = "./hello.bin"
		cfg.Remote = supervisor.RemoteConfig{}
		comp := supervisor.NewComponent(cfg, paths, install, topCfg, nil, nil, log.New("[test]", false))

		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("."))
		Expect(comp.PrepareVersion(context.Background(), ".")).To(Succeed())
	})
})
