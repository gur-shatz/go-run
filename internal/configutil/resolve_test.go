package configutil_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/configutil"
)

var _ = Describe("ResolveYAMLPath", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	It("returns the original path when the .yaml file exists", func() {
		p := filepath.Join(dir, "gorun.yaml")
		Expect(os.WriteFile(p, []byte("x"), 0644)).To(Succeed())

		Expect(configutil.ResolveYAMLPath(p)).To(Equal(p))
	})

	It("falls back to .yml when .yaml does not exist", func() {
		yamlPath := filepath.Join(dir, "gorun.yaml")
		ymlPath := filepath.Join(dir, "gorun.yml")
		Expect(os.WriteFile(ymlPath, []byte("x"), 0644)).To(Succeed())

		Expect(configutil.ResolveYAMLPath(yamlPath)).To(Equal(ymlPath))
	})

	It("falls back to .yaml when .yml does not exist", func() {
		ymlPath := filepath.Join(dir, "gorun.yml")
		yamlPath := filepath.Join(dir, "gorun.yaml")
		Expect(os.WriteFile(yamlPath, []byte("x"), 0644)).To(Succeed())

		Expect(configutil.ResolveYAMLPath(ymlPath)).To(Equal(yamlPath))
	})

	It("prefers the original extension when both exist", func() {
		yamlPath := filepath.Join(dir, "gorun.yaml")
		ymlPath := filepath.Join(dir, "gorun.yml")
		Expect(os.WriteFile(yamlPath, []byte("x"), 0644)).To(Succeed())
		Expect(os.WriteFile(ymlPath, []byte("x"), 0644)).To(Succeed())

		Expect(configutil.ResolveYAMLPath(yamlPath)).To(Equal(yamlPath))
		Expect(configutil.ResolveYAMLPath(ymlPath)).To(Equal(ymlPath))
	})

	It("returns the original path when neither exists", func() {
		p := filepath.Join(dir, "gorun.yaml")
		Expect(configutil.ResolveYAMLPath(p)).To(Equal(p))
	})

	It("returns the original path for non-yaml extensions", func() {
		p := filepath.Join(dir, "config.toml")
		Expect(configutil.ResolveYAMLPath(p)).To(Equal(p))
	})
})
