package supervisor_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("LoadConfig: file:// URL resolution", func() {
	var configDir string

	writeYAML := func(body string) string {
		path := filepath.Join(configDir, "supervisor.yml")
		Expect(os.WriteFile(path, []byte(body), 0644)).To(Succeed())
		return path
	}

	BeforeEach(func() {
		configDir = GinkgoT().TempDir()
	})

	It("rewrites a relative file:// base_url to absolute", func() {
		path := writeYAML(`
state_dir: ./state
remote:
  base_url: file://./fixture
components:
  - name: api
    port: 8080
    command: "/bin/x"
`)

		cfg, err := supervisor.LoadConfig(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Remote.BaseURL).To(Equal("file://" + filepath.Join(configDir, "fixture")))
		Expect(cfg.Components[0].Remote.BaseURL).To(Equal(cfg.Remote.BaseURL))
	})

	It("leaves an absolute file:// URL untouched", func() {
		path := writeYAML(`
state_dir: ./state
remote:
  base_url: file:///opt/origin
components:
  - name: api
    port: 8080
    command: "/bin/x"
`)

		cfg, err := supervisor.LoadConfig(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Remote.BaseURL).To(Equal("file:///opt/origin"))
	})

	It("leaves http:// URLs untouched", func() {
		path := writeYAML(`
state_dir: ./state
remote:
  base_url: http://updates.example.com
components:
  - name: api
    port: 8080
    command: "/bin/x"
`)

		cfg, err := supervisor.LoadConfig(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Remote.BaseURL).To(Equal("http://updates.example.com"))
	})

	It("resolves a per-component file:// override against the config dir", func() {
		path := writeYAML(`
state_dir: ./state
remote:
  base_url: http://updates.example.com
components:
  - name: api
    port: 8080
    command: "/bin/x"
    remote:
      base_url: file://./private-fixture
`)

		cfg, err := supervisor.LoadConfig(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Components[0].Remote.BaseURL).To(Equal("file://" + filepath.Join(configDir, "private-fixture")))
	})
})
