//go:build linux

package supervisor

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("cgroup v2 file parsers", func() {
	var dir string

	writeFile := func(name, content string) string {
		path := filepath.Join(dir, name)
		Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
		return path
	}

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	Describe("readMemoryStat", func() {
		It("parses the anon/file/slab/sock split and ignores other keys", func() {
			path := writeFile("memory.stat", "anon 1048576\nfile 2097152\nkernel 4096\nslab 65536\nsock 8192\n")
			st, ok := readMemoryStat(path)
			Expect(ok).To(BeTrue())
			Expect(st.Anon).To(Equal(int64(1048576)))
			Expect(st.File).To(Equal(int64(2097152)))
			Expect(st.Slab).To(Equal(int64(65536)))
			Expect(st.Sock).To(Equal(int64(8192)))
		})

		It("reports not-ok when the file is missing", func() {
			_, ok := readMemoryStat(filepath.Join(dir, "nope"))
			Expect(ok).To(BeFalse())
		})
	})

	Describe("readMemoryEvents", func() {
		It("parses high/max/oom_kill counters", func() {
			path := writeFile("memory.events", "low 0\nhigh 12\nmax 3\noom 1\noom_kill 2\n")
			ev, ok := readMemoryEvents(path)
			Expect(ok).To(BeTrue())
			Expect(ev.High).To(Equal(int64(12)))
			Expect(ev.Max).To(Equal(int64(3)))
			Expect(ev.OOMKill).To(Equal(int64(2)))
		})
	})

	Describe("readPSISomeAvg10", func() {
		It("returns the some avg10 as a 0..1 ratio", func() {
			path := writeFile("memory.pressure",
				"some avg10=7.00 avg60=3.50 avg300=1.00 total=123456\nfull avg10=2.00 avg60=1.00 avg300=0.50 total=6789\n")
			ratio, ok := readPSISomeAvg10(path)
			Expect(ok).To(BeTrue())
			Expect(ratio).To(BeNumerically("~", 0.07, 1e-9))
		})

		It("reports not-ok when there is no some line", func() {
			path := writeFile("memory.pressure", "full avg10=2.00 avg60=1.00 avg300=0.50 total=6789\n")
			_, ok := readPSISomeAvg10(path)
			Expect(ok).To(BeFalse())
		})
	})

	Describe("sanitizeLeafName", func() {
		It("replaces slashes so a name is a single directory component", func() {
			Expect(sanitizeLeafName("gateway")).To(Equal("gateway"))
			Expect(sanitizeLeafName("a/b")).To(Equal("a_b"))
		})
	})
})
