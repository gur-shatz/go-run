package supervisor_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

func buildSignedImage(priv ed25519.PrivateKey, bodies map[string]string) (archive, sig []byte) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range bodies {
		Expect(tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0755,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		})).To(Succeed())
		_, err := io.WriteString(tw, body)
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(tw.Close()).To(Succeed())
	Expect(gz.Close()).To(Succeed())

	archive = buf.Bytes()
	sig = ed25519.Sign(priv, archive)
	return archive, sig
}

var _ = Describe("Installer", func() {
	var (
		origin   *fakeOrigin
		server   *httptest.Server
		stateDir string
		paths    supervisor.ComponentPaths
		pub      ed25519.PublicKey
		priv     ed25519.PrivateKey
		client   *supervisor.RemoteClient
		install  *supervisor.Installer
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
		client.SetPlatform("linux", "amd64")
		install = &supervisor.Installer{Remote: client, PublicKey: pub}
	})

	AfterEach(func() { server.Close() })

	publish := func(version, bodyText string) {
		archive, sig := buildSignedImage(priv, map[string]string{
			"bin/api": bodyText,
		})
		origin.files["/api/versions/required.txt"] = version
		origin.files["/api/images/"+version+"_linux_amd64.tar.gz"] = string(archive)
		origin.files["/api/images/"+version+"_linux_amd64.tar.gz.sig"] = string(sig)
	}

	It("downloads, verifies, extracts, and writes current.txt", func() {
		publish("1.4.2", "hello")

		version, err := install.Install(context.Background(), "api",
			supervisor.RemoteConfig{BaseURL: server.URL, Target: "required.txt"}, paths)
		Expect(err).NotTo(HaveOccurred())
		Expect(version).To(Equal("1.4.2"))

		body, err := os.ReadFile(filepath.Join(paths.VersionDir("1.4.2"), "bin", "api"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("hello"))

		current, err := paths.ReadCurrent()
		Expect(err).NotTo(HaveOccurred())
		Expect(current).To(Equal("1.4.2"))
	})

	It("returns ErrAlreadyCurrent when remote version equals current.txt and the folder is on disk", func() {
		publish("1.4.2", "hello")

		_, err := install.Install(context.Background(), "api",
			supervisor.RemoteConfig{BaseURL: server.URL, Target: "required.txt"}, paths)
		Expect(err).NotTo(HaveOccurred())

		// Calling InstallVersion directly to inspect the sentinel error.
		err = install.InstallVersion(context.Background(), "api",
			supervisor.RemoteConfig{BaseURL: server.URL, Target: "required.txt"}, paths, "1.4.2")
		Expect(errors.Is(err, supervisor.ErrAlreadyCurrent)).To(BeTrue())
	})

	It("rejects a tampered archive without writing current.txt", func() {
		archive, sig := buildSignedImage(priv, map[string]string{"bin/api": "hello"})
		archive[0] ^= 0xFF // tamper
		origin.files["/api/versions/required.txt"] = "1.4.2"
		origin.files["/api/images/1.4.2_linux_amd64.tar.gz"] = string(archive)
		origin.files["/api/images/1.4.2_linux_amd64.tar.gz.sig"] = string(sig)

		_, err := install.Install(context.Background(), "api",
			supervisor.RemoteConfig{BaseURL: server.URL, Target: "required.txt"}, paths)
		Expect(err).To(MatchError(ContainSubstring("verify")))

		current, _ := paths.ReadCurrent()
		Expect(current).To(BeEmpty())
	})

	It("returns ErrVersionRejected when the resolved version is in rejects.txt", func() {
		publish("1.4.2", "hello")
		Expect(paths.AppendReject("1.4.2")).To(Succeed())

		_, err := install.Install(context.Background(), "api",
			supervisor.RemoteConfig{BaseURL: server.URL, Target: "required.txt"}, paths)
		Expect(errors.Is(err, supervisor.ErrVersionRejected)).To(BeTrue())
	})

	It("treats a 404 on the archive as a polling failure (ImageNotFoundForPlatform)", func() {
		origin.files["/api/versions/required.txt"] = "1.4.2"
		// no archive, no sig published

		_, err := install.Install(context.Background(), "api",
			supervisor.RemoteConfig{BaseURL: server.URL, Target: "required.txt"}, paths)
		Expect(errors.Is(err, supervisor.ErrImageNotFoundForPlatform)).To(BeTrue())

		// current.txt unchanged; version is not rejected.
		current, _ := paths.ReadCurrent()
		Expect(current).To(BeEmpty())

		rejected, _ := paths.IsRejected("1.4.2")
		Expect(rejected).To(BeFalse())
	})

	It("cleans up a partial extraction on failure", func() {
		// Write a tar.gz that decompresses fine but contains a tarslip — extract
		// will fail mid-way after potentially creating partial files.
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		Expect(tw.WriteHeader(&tar.Header{Name: "good", Mode: 0644, Size: 1, Typeflag: tar.TypeReg})).To(Succeed())
		_, _ = tw.Write([]byte("x"))
		Expect(tw.WriteHeader(&tar.Header{Name: "../evil", Mode: 0644, Size: 1, Typeflag: tar.TypeReg})).To(Succeed())
		_, _ = tw.Write([]byte("y"))
		Expect(tw.Close()).To(Succeed())
		Expect(gz.Close()).To(Succeed())
		body := buf.Bytes()
		bad := ed25519.Sign(priv, body)

		origin.files["/api/versions/required.txt"] = "1.4.2"
		origin.files["/api/images/1.4.2_linux_amd64.tar.gz"] = string(body)
		origin.files["/api/images/1.4.2_linux_amd64.tar.gz.sig"] = string(bad)

		_, err := install.Install(context.Background(), "api",
			supervisor.RemoteConfig{BaseURL: server.URL, Target: "required.txt"}, paths)
		Expect(err).To(HaveOccurred())

		// The version folder must NOT remain.
		_, statErr := os.Stat(paths.VersionDir("1.4.2"))
		Expect(os.IsNotExist(statErr)).To(BeTrue())
	})

	It("works with a file:// remote and no signature when PublicKey is nil", func() {
		// Build the on-disk layout the supervisor expects under a tmpdir.
		root := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(root, "api", "versions"), 0755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(root, "api", "images"), 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "api", "versions", "required.txt"), []byte("1.4.2\n"), 0644)).To(Succeed())

		// Use the same helper that signs; we just ignore the sig file on disk.
		archive, _ := buildSignedImage(priv, map[string]string{"bin/api": "hello"})
		// The test client is pinned to linux/amd64 via SetPlatform above.
		archiveName := "1.4.2_linux_amd64.tar.gz"
		Expect(os.WriteFile(filepath.Join(root, "api", "images", archiveName), archive, 0644)).To(Succeed())

		// Installer with no public key — verification is skipped.
		fsInstaller := &supervisor.Installer{Remote: client, PublicKey: nil}

		version, err := fsInstaller.Install(context.Background(), "api",
			supervisor.RemoteConfig{BaseURL: "file://" + root, Target: "required.txt"}, paths)
		Expect(err).NotTo(HaveOccurred())
		Expect(version).To(Equal("1.4.2"))

		body, err := os.ReadFile(filepath.Join(paths.VersionDir("1.4.2"), "bin", "api"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("hello"))
	})

	It("reports a missing file:// archive as ErrImageNotFoundForPlatform", func() {
		root := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(root, "api", "versions"), 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "api", "versions", "required.txt"), []byte("1.4.2\n"), 0644)).To(Succeed())
		// No images/ contents.

		fsInstaller := &supervisor.Installer{Remote: client, PublicKey: nil}

		_, err := fsInstaller.Install(context.Background(), "api",
			supervisor.RemoteConfig{BaseURL: "file://" + root, Target: "required.txt"}, paths)
		Expect(errors.Is(err, supervisor.ErrImageNotFoundForPlatform)).To(BeTrue())

		current, _ := paths.ReadCurrent()
		Expect(current).To(BeEmpty())
	})

	It("supports a bearer token via RemoteClient", func() {
		// override the origin to require a bearer.
		bearerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer s3cr3t" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			origin.serve()(w, r)
		}))
		defer bearerServer.Close()
		publish("1.4.2", "hello")

		authedClient := supervisor.NewRemoteClient("s3cr3t")
		authedClient.SetPlatform("linux", "amd64")
		authedInstall := &supervisor.Installer{Remote: authedClient, PublicKey: pub}

		_, err := authedInstall.Install(context.Background(), "api",
			supervisor.RemoteConfig{BaseURL: bearerServer.URL, Target: "required.txt"}, paths)
		Expect(err).NotTo(HaveOccurred())
	})
})
