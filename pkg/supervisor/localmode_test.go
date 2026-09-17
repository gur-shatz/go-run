package supervisor_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/pkg/supervisor"
)

// Locally directed mode: target.txt (app-written) names the version,
// location.yml (operator-written) may redirect the remote, and rejects.txt
// still wins over target.txt.
var _ = Describe("Locally directed update mode", func() {
	var (
		origin   *fakeOrigin
		server   *httptest.Server
		stateDir string
		paths    supervisor.ComponentPaths
		pub      ed25519.PublicKey
		priv     ed25519.PrivateKey
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

		client := supervisor.NewRemoteClient("")
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
			Updates:                supervisor.UpdatesConfig{Mode: supervisor.UpdateModeLocallyDirected},
		}
		topCfg.ApplyDefaults()
	})

	AfterEach(func() { server.Close() })

	mkCfg := func(baseURL string) supervisor.ComponentConfig {
		return supervisor.ComponentConfig{
			Name:    "api",
			Port:    freeTCPPort(),
			Command: "/bin/sh ./run.sh",
			Remote: supervisor.RemoteConfig{
				Enabled:         true,
				EnabledSet:      true,
				BaseURL:         baseURL,
				Target:          "required.txt",
				PollingInterval: 100 * time.Millisecond,
			},
		}
	}

	writeTarget := func(body string) {
		Expect(os.WriteFile(paths.Target(), []byte(body), 0644)).To(Succeed())
	}

	newComp := func(cfg supervisor.ComponentConfig) *supervisor.Component {
		return supervisor.NewComponent(cfg, paths, install, topCfg, nil, nil, log.New("[test]", false))
	}

	It("installs and runs the version target.txt names, and follows a rewrite", func() {
		publishWithBinary(origin, priv, "1.0.0")
		publishWithBinary(origin, priv, "2.0.0")
		origin.files["/api/versions/required.txt"] = "9.9.9" // must be ignored in this mode
		writeTarget("1.0.0\n")

		comp := newComp(mkCfg(server.URL))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { _ = comp.Run(ctx); close(done) }()

		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())
		Expect(comp.Snapshot().Current).To(Equal("1.0.0"))

		writeTarget("2.0.0\n")
		Eventually(func() string { return comp.Snapshot().Current }, 3*time.Second).Should(Equal("2.0.0"))

		cancel()
		Eventually(done, 3*time.Second).Should(BeClosed())
	})

	It("exports OP_TARGET_FILE and OP_LOCATION_FILE to the child", func() {
		body := "#!/bin/sh\necho \"$OP_TARGET_FILE\" > \"$OP_STATE_DIR/seen_target\"\necho \"$OP_LOCATION_FILE\" > \"$OP_STATE_DIR/seen_location\"\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n"
		archive, sig := buildSignedImage(priv, map[string]string{"run.sh": body})
		origin.files["/api/images/1.0.0_"+runtimeGOOS()+"_"+runtimeGOARCH()+".tar.gz"] = string(archive)
		origin.files["/api/images/1.0.0_"+runtimeGOOS()+"_"+runtimeGOARCH()+".tar.gz.sig"] = string(sig)
		writeTarget("1.0.0\n")

		comp := newComp(mkCfg(server.URL))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { _ = comp.Run(ctx); close(done) }()

		Eventually(func() string {
			b, _ := os.ReadFile(filepath.Join(paths.Root, "seen_target"))
			return string(b)
		}, 3*time.Second).Should(Equal(paths.Target() + "\n"))
		b, _ := os.ReadFile(filepath.Join(paths.Root, "seen_location"))
		Expect(string(b)).To(Equal(paths.Location() + "\n"))

		cancel()
		Eventually(done, 3*time.Second).Should(BeClosed())
	})

	It("runs current.txt when target.txt is absent", func() {
		Expect(paths.WriteCurrent("1.0.0")).To(Succeed())
		comp := newComp(mkCfg(server.URL))
		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("1.0.0"))
	})

	It("has nothing to run when neither target.txt nor current.txt exists", func() {
		comp := newComp(mkCfg(server.URL))
		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(BeEmpty())
	})

	It("holds current when target.txt names a rejected version", func() {
		Expect(paths.WriteCurrent("1.0.0")).To(Succeed())
		Expect(paths.AppendReject("2.0.0")).To(Succeed())
		writeTarget("2.0.0\n")

		comp := newComp(mkCfg(server.URL))
		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(BeEmpty(), "a rejected local target must not be prepared")
	})

	It("lets forced_versions.txt override target.txt", func() {
		Expect(paths.WriteCurrent("1.0.0")).To(Succeed())
		writeTarget("3.0.0\n")

		forced := func() supervisor.ForcedOverride {
			return supervisor.ForcedOverride{Kind: supervisor.ForcedKindVersion, Version: "2.0.0"}
		}
		comp := supervisor.NewComponent(mkCfg(server.URL), paths, install, topCfg, forced, nil, log.New("[test]", false))
		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("2.0.0"))
	})

	It("keeps the last good target when target.txt becomes unparsable", func() {
		writeTarget("1.0.0\n")
		comp := newComp(mkCfg(server.URL))
		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("1.0.0"))

		writeTarget("../../etc\n")
		target, err = comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("1.0.0"))
	})

	It("holds current when target.txt says stable and there is no stable.txt", func() {
		Expect(paths.WriteCurrent("1.0.0")).To(Succeed())
		writeTarget("stable\n")
		comp := newComp(mkCfg(server.URL))
		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("1.0.0"))

		Expect(paths.WriteStable("0.9.0")).To(Succeed())
		target, err = comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("0.9.0"))
	})

	It("resolves an @pointer at the remote", func() {
		origin.files["/api/versions/canary.txt"] = "@canary2.txt"
		origin.files["/api/versions/canary2.txt"] = "1.2.3\n"
		writeTarget("@canary.txt\n")
		comp := newComp(mkCfg(server.URL))
		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("1.2.3"))
	})

	It("fetches from location.yml with its own bearer when the remote block has no base_url", func() {
		bearerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tenant-42" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			origin.serve()(w, r)
		}))
		defer bearerServer.Close()
		publishWithBinary(origin, priv, "1.0.0")
		Expect(os.WriteFile(paths.Location(), []byte("base_url: "+bearerServer.URL+"\nsecret: tenant-42\n"), 0600)).To(Succeed())
		writeTarget("1.0.0\n")

		comp := newComp(mkCfg(""))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() { _ = comp.Run(ctx); close(done) }()

		Eventually(func() string { return comp.Snapshot().Current }, 3*time.Second).Should(Equal("1.0.0"))
		Eventually(func() int { return comp.Snapshot().ChildPID }, 3*time.Second).ShouldNot(BeZero())

		cancel()
		Eventually(done, 3*time.Second).Should(BeClosed())
	})

	It("fails prepare with a clear error when no remote is known at all", func() {
		writeTarget("1.0.0\n")
		comp := newComp(mkCfg(""))
		target, err := comp.ComputeDesiredVersionForTest(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(target).To(Equal("1.0.0"))
		Expect(comp.PrepareVersion(context.Background(), target)).To(MatchError(ContainSubstring("no remote configured")))
	})
})
