package sumfile_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/sumfile"
)

var _ = Describe("Sumfile", func() {
	var tmpDir string

	BeforeEach(func() {
		tmpDir = GinkgoT().TempDir()
	})

	Describe("Write and Read", func() {
		It("round-trips entries correctly", func() {
			path := filepath.Join(tmpDir, "test.sum")
			entries := map[string]string{
				"cmd/server/main.go":    "a1b2c3d",
				"internal/handler.go":   "e4f5678",
				"go.mod":                "9abcdef",
			}

			Expect(sumfile.Write(path, entries)).To(Succeed())

			got, err := sumfile.Read(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(entries))
		})

		It("writes entries sorted alphabetically", func() {
			path := filepath.Join(tmpDir, "test.sum")
			entries := map[string]string{
				"z.go": "1111111",
				"a.go": "2222222",
				"m.go": "3333333",
			}

			Expect(sumfile.Write(path, entries)).To(Succeed())

			content, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(Equal("a.go 2222222\nm.go 3333333\nz.go 1111111\n"))
		})

		It("returns nil for non-existent file", func() {
			got, err := sumfile.Read(filepath.Join(tmpDir, "nope.sum"))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeNil())
		})
	})

	Describe("Diff", func() {
		It("detects added files", func() {
			old := map[string]string{"a.go": "1111111"}
			new := map[string]string{"a.go": "1111111", "b.go": "2222222"}

			cs := sumfile.Diff(old, new)
			Expect(cs.Added).To(Equal([]string{"b.go"}))
			Expect(cs.Modified).To(BeEmpty())
			Expect(cs.Removed).To(BeEmpty())
		})

		It("detects modified files", func() {
			old := map[string]string{"a.go": "1111111"}
			new := map[string]string{"a.go": "2222222"}

			cs := sumfile.Diff(old, new)
			Expect(cs.Added).To(BeEmpty())
			Expect(cs.Modified).To(Equal([]string{"a.go"}))
			Expect(cs.Removed).To(BeEmpty())
		})

		It("detects removed files", func() {
			old := map[string]string{"a.go": "1111111", "b.go": "2222222"}
			new := map[string]string{"a.go": "1111111"}

			cs := sumfile.Diff(old, new)
			Expect(cs.Added).To(BeEmpty())
			Expect(cs.Modified).To(BeEmpty())
			Expect(cs.Removed).To(Equal([]string{"b.go"}))
		})

		It("detects mixed changes", func() {
			old := map[string]string{"a.go": "1111111", "b.go": "2222222"}
			new := map[string]string{"a.go": "3333333", "c.go": "4444444"}

			cs := sumfile.Diff(old, new)
			Expect(cs.Added).To(Equal([]string{"c.go"}))
			Expect(cs.Modified).To(Equal([]string{"a.go"}))
			Expect(cs.Removed).To(Equal([]string{"b.go"}))
		})

		It("reports empty changeset when nothing changed", func() {
			entries := map[string]string{"a.go": "1111111"}
			cs := sumfile.Diff(entries, entries)
			Expect(cs.IsEmpty()).To(BeTrue())
		})

		It("handles nil old map (initial scan)", func() {
			new := map[string]string{"a.go": "1111111", "b.go": "2222222"}
			cs := sumfile.Diff(nil, new)
			Expect(cs.Added).To(ConsistOf("a.go", "b.go"))
		})
	})
})
