package supervisor_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("ForcedOverrides", func() {
	var stateDir string

	BeforeEach(func() {
		stateDir = GinkgoT().TempDir()
	})

	writeForced := func(body string) string {
		path := filepath.Join(stateDir, "forced_versions.txt")
		Expect(os.WriteFile(path, []byte(body), 0644)).To(Succeed())
		return path
	}

	It("is empty when the file does not exist", func() {
		f, err := supervisor.ReadForcedOverrides(filepath.Join(stateDir, "forced_versions.txt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(f.HasAny()).To(BeFalse())
		Expect(f.Lookup("anything").Kind).To(Equal(supervisor.ForcedKindNone))
	})

	It("parses per-component version and stable pins", func() {
		path := writeForced("component-a = 1.4.2\ncomponent-b = stable\n")

		f, err := supervisor.ReadForcedOverrides(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.HasAny()).To(BeTrue())

		a := f.Lookup("component-a")
		Expect(a.Kind).To(Equal(supervisor.ForcedKindVersion))
		Expect(a.Version).To(Equal("1.4.2"))

		b := f.Lookup("component-b")
		Expect(b.Kind).To(Equal(supervisor.ForcedKindStable))
	})

	It("applies the wildcard to components without an explicit entry", func() {
		path := writeForced("* = stable\ncomponent-a = 1.4.2\n")

		f, err := supervisor.ReadForcedOverrides(path)
		Expect(err).NotTo(HaveOccurred())

		Expect(f.Lookup("component-a").Kind).To(Equal(supervisor.ForcedKindVersion))
		Expect(f.Lookup("component-a").Version).To(Equal("1.4.2"))

		other := f.Lookup("component-z")
		Expect(other.Kind).To(Equal(supervisor.ForcedKindStable))
	})

	It("ignores blanks and comments, including trailing inline comments", func() {
		path := writeForced(`
# pin api to a hot-fix
component-a = 1.4.2   # awaiting investigation

# everything else stays put
* = stable
`)
		f, err := supervisor.ReadForcedOverrides(path)
		Expect(err).NotTo(HaveOccurred())

		Expect(f.Lookup("component-a").Version).To(Equal("1.4.2"))
		Expect(f.Lookup("anything").Kind).To(Equal(supervisor.ForcedKindStable))
	})

	It("returns a parse error with the line number when a value is missing", func() {
		path := writeForced("component-a\n")
		_, err := supervisor.ReadForcedOverrides(path)
		Expect(err).To(MatchError(ContainSubstring("missing '='")))
		Expect(err.Error()).To(ContainSubstring(":1:"))
	})

	It("rejects an empty value", func() {
		path := writeForced("component-a =\n")
		_, err := supervisor.ReadForcedOverrides(path)
		Expect(err).To(MatchError(ContainSubstring("empty value")))
	})
})
