package supervisor_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("State files", func() {
	var (
		stateDir string
		comp     supervisor.ComponentPaths
	)

	BeforeEach(func() {
		stateDir = GinkgoT().TempDir()
		comp = supervisor.NewPaths(stateDir).Component("api")
		Expect(comp.EnsureDirs()).To(Succeed())
	})

	Describe("stable.txt / current.txt round-trip", func() {
		It("returns empty when the files do not exist", func() {
			v, err := comp.ReadStable()
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(BeEmpty())

			v, err = comp.ReadCurrent()
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(BeEmpty())
		})

		It("writes and reads the version atomically", func() {
			Expect(comp.WriteStable("1.4.2")).To(Succeed())
			v, err := comp.ReadStable()
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal("1.4.2"))

			Expect(comp.WriteCurrent("1.4.3")).To(Succeed())
			v, err = comp.ReadCurrent()
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal("1.4.3"))
		})

		It("overwrites the previous value on a second write", func() {
			Expect(comp.WriteStable("1.4.2")).To(Succeed())
			Expect(comp.WriteStable("1.5.0")).To(Succeed())

			v, err := comp.ReadStable()
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal("1.5.0"))
		})

		It("trims trailing whitespace and newlines when reading external writes", func() {
			Expect(os.WriteFile(comp.Stable(), []byte("1.4.2\r\n"), 0644)).To(Succeed())

			v, err := comp.ReadStable()
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal("1.4.2"))
		})

		It("does not leak temp files in the component dir on success", func() {
			Expect(comp.WriteStable("1.4.2")).To(Succeed())

			entries, err := os.ReadDir(comp.Root)
			Expect(err).NotTo(HaveOccurred())
			for _, e := range entries {
				Expect(filepath.Ext(e.Name())).NotTo(Equal(".tmp"))
			}
		})
	})

	Describe("rejects.txt", func() {
		It("is empty when missing", func() {
			rejects, err := comp.ReadRejects()
			Expect(err).NotTo(HaveOccurred())
			Expect(rejects).To(BeEmpty())
		})

		It("appends and reads back versions", func() {
			Expect(comp.AppendReject("1.4.2")).To(Succeed())
			Expect(comp.AppendReject("1.5.0")).To(Succeed())

			rejects, err := comp.ReadRejects()
			Expect(err).NotTo(HaveOccurred())
			Expect(rejects).To(Equal([]string{"1.4.2", "1.5.0"}))
		})

		It("deduplicates a repeat append", func() {
			Expect(comp.AppendReject("1.4.2")).To(Succeed())
			Expect(comp.AppendReject("1.4.2")).To(Succeed())

			rejects, err := comp.ReadRejects()
			Expect(err).NotTo(HaveOccurred())
			Expect(rejects).To(Equal([]string{"1.4.2"}))
		})

		It("skips comments and blanks on read", func() {
			Expect(os.WriteFile(comp.Rejects(),
				[]byte("# bad ones\n1.4.2\n\n# also bad\n1.5.0\n"), 0644)).To(Succeed())

			rejects, err := comp.ReadRejects()
			Expect(err).NotTo(HaveOccurred())
			Expect(rejects).To(Equal([]string{"1.4.2", "1.5.0"}))
		})

		It("reports membership via IsRejected", func() {
			Expect(comp.AppendReject("1.4.2")).To(Succeed())

			yes, err := comp.IsRejected("1.4.2")
			Expect(err).NotTo(HaveOccurred())
			Expect(yes).To(BeTrue())

			no, err := comp.IsRejected("1.5.0")
			Expect(err).NotTo(HaveOccurred())
			Expect(no).To(BeFalse())
		})
	})
})
