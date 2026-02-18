package hasher_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/hasher"
)

var _ = Describe("Hasher", func() {
	var tmpDir string

	BeforeEach(func() {
		tmpDir = GinkgoT().TempDir()
	})

	Describe("HashFile", func() {
		It("returns a 7-character hex string", func() {
			path := filepath.Join(tmpDir, "test.go")
			Expect(os.WriteFile(path, []byte("package main\n"), 0644)).To(Succeed())

			hash, err := hasher.HashFile(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(hash).To(HaveLen(7))
			Expect(hash).To(MatchRegexp("^[0-9a-f]{7}$"))
		})

		It("returns the same hash for the same content", func() {
			p1 := filepath.Join(tmpDir, "a.go")
			p2 := filepath.Join(tmpDir, "b.go")
			content := []byte("package main\n")
			Expect(os.WriteFile(p1, content, 0644)).To(Succeed())
			Expect(os.WriteFile(p2, content, 0644)).To(Succeed())

			h1, err := hasher.HashFile(p1)
			Expect(err).NotTo(HaveOccurred())
			h2, err := hasher.HashFile(p2)
			Expect(err).NotTo(HaveOccurred())
			Expect(h1).To(Equal(h2))
		})

		It("returns different hashes for different content", func() {
			p1 := filepath.Join(tmpDir, "a.go")
			p2 := filepath.Join(tmpDir, "b.go")
			Expect(os.WriteFile(p1, []byte("package a\n"), 0644)).To(Succeed())
			Expect(os.WriteFile(p2, []byte("package b\n"), 0644)).To(Succeed())

			h1, err := hasher.HashFile(p1)
			Expect(err).NotTo(HaveOccurred())
			h2, err := hasher.HashFile(p2)
			Expect(err).NotTo(HaveOccurred())
			Expect(h1).NotTo(Equal(h2))
		})

		It("returns an error for non-existent file", func() {
			_, err := hasher.HashFile(filepath.Join(tmpDir, "nope.go"))
			Expect(err).To(HaveOccurred())
		})
	})
})
