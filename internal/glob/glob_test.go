package glob_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/glob"
)

var _ = Describe("Glob", func() {
	var tmpDir string

	BeforeEach(func() {
		tmpDir = GinkgoT().TempDir()
	})

	Describe("ExpandPatterns", func() {
		It("expands glob patterns to matching files", func() {
			Expect(os.MkdirAll(filepath.Join(tmpDir, "cmd"), 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(tmpDir, "cmd", "app.go"), []byte("package cmd"), 0644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test"), 0644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(tmpDir, "readme.md"), []byte("# readme"), 0644)).To(Succeed())

			patterns := []glob.Pattern{
				{Raw: "**/*.go"},
				{Raw: "go.mod"},
			}

			files, err := glob.ExpandPatterns(tmpDir, patterns)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(ConsistOf("cmd/app.go", "go.mod", "main.go"))
		})

		It("excludes negated patterns", func() {
			Expect(os.MkdirAll(filepath.Join(tmpDir, "gen"), 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(tmpDir, "gen", "service.pb.go"), []byte("package gen"), 0644)).To(Succeed())

			patterns := []glob.Pattern{
				{Raw: "**/*.go"},
				{Raw: "**/*.pb.go", Negated: true},
			}

			files, err := glob.ExpandPatterns(tmpDir, patterns)
			Expect(err).NotTo(HaveOccurred())
			Expect(files).To(ConsistOf("main.go"))
		})
	})
})
