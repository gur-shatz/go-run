package supervisor

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("renderVersionTemplates", func() {
	var (
		versionDir string
		launchVars LaunchVars
	)

	BeforeEach(func() {
		versionDir = GinkgoT().TempDir()
		launchVars = LaunchVars{
			Version:     "v1",
			VersionDir:  versionDir,
			StateDir:    "/tmp/state/hello",
			MonitorPort: 18090,
			KillSock:    "/tmp/state/hello/kill.sock",
		}
	})

	writeFile := func(rel, body string, mode os.FileMode) string {
		path := filepath.Join(versionDir, rel)
		Expect(os.MkdirAll(filepath.Dir(path), 0755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(body), mode)).To(Succeed())
		return path
	}

	readFile := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(versionDir, rel))
		Expect(err).NotTo(HaveOccurred())
		return string(data)
	}

	It("renders a .tmpl using supervisor vars and strips the suffix", func() {
		writeFile("config.toml.tmpl", `greeting = "{{ .GREETING }}"`, 0644)

		err := renderVersionTemplates(versionDir, map[string]any{"GREETING": "hi from vars"}, launchVars)
		Expect(err).NotTo(HaveOccurred())
		Expect(readFile("config.toml")).To(Equal(`greeting = "hi from vars"`))
	})

	It("supervisor vars override defaults.yml", func() {
		writeFile("defaults.yml", "GREETING: from defaults\n", 0644)
		writeFile("config.toml.tmpl", `g = "{{ .GREETING }}"`, 0644)

		err := renderVersionTemplates(versionDir, map[string]any{"GREETING": "from vars"}, launchVars)
		Expect(err).NotTo(HaveOccurred())
		Expect(readFile("config.toml")).To(Equal(`g = "from vars"`))
	})

	It("falls back to defaults.yml when supervisor vars don't set the key", func() {
		writeFile("defaults.yml", "LOG_LEVEL: info\n", 0644)
		writeFile("conf.tmpl", `level = "{{ .LOG_LEVEL }}"`, 0644)

		err := renderVersionTemplates(versionDir, nil, launchVars)
		Expect(err).NotTo(HaveOccurred())
		Expect(readFile("conf")).To(Equal(`level = "info"`))
	})

	It("exposes the five launch vars as template keys", func() {
		writeFile("paths.toml.tmpl", `dir = "{{ .VERSION_DIR }}"; port = "{{ .MONITOR_PORT }}"`, 0644)

		err := renderVersionTemplates(versionDir, nil, launchVars)
		Expect(err).NotTo(HaveOccurred())
		Expect(readFile("paths.toml")).To(Equal(`dir = "` + versionDir + `"; port = "18090"`))
	})

	It("renders nested .tmpl files in subdirectories", func() {
		writeFile("subdir/inner/foo.conf.tmpl", `x = "{{ .X }}"`, 0644)

		err := renderVersionTemplates(versionDir, map[string]any{"X": "nested"}, launchVars)
		Expect(err).NotTo(HaveOccurred())
		Expect(readFile("subdir/inner/foo.conf")).To(Equal(`x = "nested"`))
	})

	It("rotates a previous render to .bak on re-render", func() {
		writeFile("config.toml.tmpl", `n = "{{ .N }}"`, 0644)

		Expect(renderVersionTemplates(versionDir, map[string]any{"N": "first"}, launchVars)).To(Succeed())
		Expect(readFile("config.toml")).To(Equal(`n = "first"`))

		Expect(renderVersionTemplates(versionDir, map[string]any{"N": "second"}, launchVars)).To(Succeed())
		Expect(readFile("config.toml")).To(Equal(`n = "second"`))
		Expect(readFile("config.toml.bak")).To(Equal(`n = "first"`))
	})

	It("replaces an existing .bak on the next rotation (single-level)", func() {
		writeFile("config.toml.tmpl", `n = "{{ .N }}"`, 0644)

		Expect(renderVersionTemplates(versionDir, map[string]any{"N": "first"}, launchVars)).To(Succeed())
		Expect(renderVersionTemplates(versionDir, map[string]any{"N": "second"}, launchVars)).To(Succeed())
		Expect(renderVersionTemplates(versionDir, map[string]any{"N": "third"}, launchVars)).To(Succeed())

		Expect(readFile("config.toml")).To(Equal(`n = "third"`))
		Expect(readFile("config.toml.bak")).To(Equal(`n = "second"`))
		_, err := os.Stat(filepath.Join(versionDir, "config.toml.bak.bak"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("preserves the .tmpl file's mode on the rendered output", func() {
		writeFile("run.sh.tmpl", "#!/bin/sh\necho {{ .MSG }}\n", 0755)

		err := renderVersionTemplates(versionDir, map[string]any{"MSG": "hello"}, launchVars)
		Expect(err).NotTo(HaveOccurred())

		info, statErr := os.Stat(filepath.Join(versionDir, "run.sh"))
		Expect(statErr).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0755)))
	})

	It("works with no defaults.yml at all", func() {
		writeFile("config.toml.tmpl", `x = "{{ .X | default "fallback" }}"`, 0644)

		err := renderVersionTemplates(versionDir, nil, launchVars)
		Expect(err).NotTo(HaveOccurred())
		Expect(readFile("config.toml")).To(Equal(`x = "fallback"`))
	})

	It("returns a useful error on a syntactically-broken template", func() {
		writeFile("broken.toml.tmpl", `x = "{{ .X `, 0644)

		err := renderVersionTemplates(versionDir, nil, launchVars)
		Expect(err).To(MatchError(ContainSubstring("broken.toml.tmpl")))
	})

	It("returns a useful error for a required-but-missing var", func() {
		writeFile("config.toml.tmpl", `x = "{{ required "X is required" .X }}"`, 0644)

		err := renderVersionTemplates(versionDir, nil, launchVars)
		Expect(err).To(MatchError(ContainSubstring("X is required")))
	})

	It("fails when an undefined var leaves <no value> in the output", func() {
		writeFile("config.toml.tmpl", `x = "{{ .UNDEFINED }}"`, 0644)

		err := renderVersionTemplates(versionDir, nil, launchVars)
		Expect(err).To(MatchError(ContainSubstring("unresolved template variable")))
		// The target must NOT have been written.
		_, statErr := os.Stat(filepath.Join(versionDir, "config.toml"))
		Expect(os.IsNotExist(statErr)).To(BeTrue())
	})

	It("validates rendered .yml output", func() {
		writeFile("ok.yml.tmpl", `name: {{ .NAME }}`, 0644)
		Expect(renderVersionTemplates(versionDir, map[string]any{"NAME": "hello"}, launchVars)).To(Succeed())

		writeFile("bad.yml.tmpl", `name: [unbalanced`, 0644)
		err := renderVersionTemplates(versionDir, nil, launchVars)
		Expect(err).To(MatchError(ContainSubstring("bad.yml")))
	})

	It("validates rendered .json output", func() {
		writeFile("bad.json.tmpl", `{"k": {{ .V }}`, 0644)
		err := renderVersionTemplates(versionDir, map[string]any{"V": "missing-closing-brace"}, launchVars)
		Expect(err).To(MatchError(ContainSubstring("bad.json")))
	})

	It("doesn't validate arbitrary extensions like .conf or .txt", func() {
		writeFile("greeting.txt.tmpl", `hi: [oops without close brackets`, 0644)
		Expect(renderVersionTemplates(versionDir, nil, launchVars)).To(Succeed())
		Expect(readFile("greeting.txt")).To(Equal(`hi: [oops without close brackets`))
	})

	It("is idempotent — re-rendering with the same context produces identical bytes", func() {
		writeFile("c.tmpl", `{{ .N }}`, 0644)

		Expect(renderVersionTemplates(versionDir, map[string]any{"N": 42}, launchVars)).To(Succeed())
		first := readFile("c")

		// Second render: same inputs, same output. .bak gets the same content too.
		Expect(renderVersionTemplates(versionDir, map[string]any{"N": 42}, launchVars)).To(Succeed())
		Expect(readFile("c")).To(Equal(first))
		Expect(readFile("c.bak")).To(Equal(first))
	})

	It("skips non-.tmpl files (does not touch the binary alongside templates)", func() {
		writeFile("bin/hello", "binary bytes", 0755)
		writeFile("config.toml.tmpl", `g = "x"`, 0644)

		Expect(renderVersionTemplates(versionDir, nil, launchVars)).To(Succeed())
		// The binary is untouched.
		Expect(readFile("bin/hello")).To(Equal("binary bytes"))
	})
})
