package supervisor_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("ReadLocalTarget", func() {
	var dir string

	BeforeEach(func() { dir = GinkgoT().TempDir() })

	write := func(body string) string {
		path := filepath.Join(dir, "target.txt")
		Expect(os.WriteFile(path, []byte(body), 0644)).To(Succeed())
		return path
	}

	It("is none when the file is absent", func() {
		lt, err := supervisor.ReadLocalTarget(filepath.Join(dir, "target.txt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lt.Kind).To(Equal(supervisor.LocalTargetNone))
	})

	It("is none when the file is empty or whitespace", func() {
		lt, err := supervisor.ReadLocalTarget(write("  \n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lt.Kind).To(Equal(supervisor.LocalTargetNone))
	})

	It("parses a concrete version with surrounding whitespace", func() {
		lt, err := supervisor.ReadLocalTarget(write("1.4.2\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lt.Kind).To(Equal(supervisor.LocalTargetVersion))
		Expect(lt.Version).To(Equal("1.4.2"))
		Expect(lt.String()).To(Equal("1.4.2"))
	})

	It("parses the stable keyword", func() {
		lt, err := supervisor.ReadLocalTarget(write("stable\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lt.Kind).To(Equal(supervisor.LocalTargetStable))
	})

	It("parses an @pointer", func() {
		lt, err := supervisor.ReadLocalTarget(write("@required.txt\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lt.Kind).To(Equal(supervisor.LocalTargetPointer))
		Expect(lt.Version).To(Equal("required.txt"))
		Expect(lt.String()).To(Equal("@required.txt"))
	})

	It("rejects multi-line files", func() {
		_, err := supervisor.ReadLocalTarget(write("1.4.2\n1.4.3\n"))
		Expect(err).To(MatchError(ContainSubstring("single line")))
	})

	DescribeTable("rejects names that are not safe version folder names",
		func(body string) {
			_, err := supervisor.ReadLocalTarget(write(body))
			Expect(err).To(HaveOccurred())
		},
		Entry("path traversal", "../../etc\n"),
		Entry("slash", "1.4/2\n"),
		Entry("backslash", "1.4\\2\n"),
		Entry("dot", ".\n"),
		Entry("inner whitespace", "1.4 2\n"),
		Entry("bare @", "@\n"),
		Entry("pointer with slash", "@../required.txt\n"),
	)
})

var _ = Describe("ReadLocation", func() {
	var dir string

	BeforeEach(func() { dir = GinkgoT().TempDir() })

	write := func(body string) string {
		path := filepath.Join(dir, "location.yml")
		Expect(os.WriteFile(path, []byte(body), 0644)).To(Succeed())
		return path
	}

	It("reports absence without error", func() {
		_, present, err := supervisor.ReadLocation(filepath.Join(dir, "location.yml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(present).To(BeFalse())
	})

	It("parses base_url and an inline secret", func() {
		loc, present, err := supervisor.ReadLocation(write("base_url: https://updates.example.com/t42\nsecret: abc\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(present).To(BeTrue())
		Expect(loc.BaseURL).To(Equal("https://updates.example.com/t42"))
		Expect(loc.Secret).To(Equal("abc"))
	})

	It("resolves secret_env", func() {
		GinkgoT().Setenv("GO_RUN_TEST_LOCATION_SECRET", "from-env")
		loc, _, err := supervisor.ReadLocation(write("base_url: https://x.example.com\nsecret_env: GO_RUN_TEST_LOCATION_SECRET\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(loc.Secret).To(Equal("from-env"))
	})

	It("resolves secret_file, trimmed", func() {
		secretPath := filepath.Join(dir, "bearer")
		Expect(os.WriteFile(secretPath, []byte("from-file\n"), 0600)).To(Succeed())
		loc, _, err := supervisor.ReadLocation(write("base_url: https://x.example.com\nsecret_file: " + secretPath + "\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(loc.Secret).To(Equal("from-file"))
	})

	It("fails when secret_file is missing", func() {
		_, _, err := supervisor.ReadLocation(write("base_url: https://x.example.com\nsecret_file: " + filepath.Join(dir, "nope") + "\n"))
		Expect(err).To(MatchError(ContainSubstring("secret_file")))
	})

	It("accepts an absolute file:// base_url", func() {
		_, present, err := supervisor.ReadLocation(write("base_url: file:///srv/updates\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(present).To(BeTrue())
	})

	DescribeTable("rejects bad inputs",
		func(body, want string) {
			_, _, err := supervisor.ReadLocation(write(body))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("missing base_url", "secret: abc\n", "base_url is required"),
		Entry("bad scheme", "base_url: ftp://x\n", "http:// or https://"),
		Entry("relative file url", "base_url: file://rel/path\n", "absolute"),
		Entry("not yaml", "base_url: [\n", "parse"),
	)

	It("applies only base_url and secret onto a RemoteConfig", func() {
		loc := supervisor.Location{BaseURL: "https://b", Secret: "s"}
		out := loc.Apply(supervisor.RemoteConfig{BaseURL: "https://a", Secret: "x", Target: "required.txt", SignaturePublicKeyPath: "/k"})
		Expect(out.BaseURL).To(Equal("https://b"))
		Expect(out.Secret).To(Equal("s"))
		Expect(out.Target).To(Equal("required.txt"))
		Expect(out.SignaturePublicKeyPath).To(Equal("/k"))
	})

	It("keeps the configured bearer when the location names none", func() {
		loc := supervisor.Location{BaseURL: "https://b"}
		out := loc.Apply(supervisor.RemoteConfig{BaseURL: "https://a", Secret: "x"})
		Expect(out.BaseURL).To(Equal("https://b"))
		Expect(out.Secret).To(Equal("x"))
	})
})
