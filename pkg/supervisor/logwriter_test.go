package supervisor

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("rotatingFile", func() {
	var (
		dir  string
		path string
	)

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		path = filepath.Join(dir, "stdout.log")
	})

	It("appends within the cap without rotating", func() {
		w, err := openRotatingFile(path, 1024, 3)
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write([]byte("hello\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())

		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("hello\n"))
		_, err = os.Stat(path + ".1")
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("rotates when a write would exceed the cap", func() {
		w, err := openRotatingFile(path, 10, 3)
		Expect(err).NotTo(HaveOccurred())
		// Fill the active file.
		_, err = w.Write([]byte("0123456789")) // exactly the cap
		Expect(err).NotTo(HaveOccurred())
		// Next write triggers rotation.
		_, err = w.Write([]byte("X"))
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())

		// .1 has the original content.
		rotated, err := os.ReadFile(path + ".1")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(rotated)).To(Equal("0123456789"))

		// Active file has the trigger-byte.
		current, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(current)).To(Equal("X"))
	})

	It("shifts existing generations and drops past maxFiles", func() {
		w, err := openRotatingFile(path, 4, 2) // maxFiles=2 → keep .1, .2
		Expect(err).NotTo(HaveOccurred())

		writeAndRotate := func(content string) {
			_, err := w.Write([]byte(content))
			Expect(err).NotTo(HaveOccurred())
		}

		// 4 generations of content; .3 must not exist by the end.
		writeAndRotate("AAAA") // active=AAAA
		writeAndRotate("BBBB") // → .1=AAAA, active=BBBB
		writeAndRotate("CCCC") // → .2=AAAA, .1=BBBB, active=CCCC
		writeAndRotate("DDDD") // → .2=BBBB, .1=CCCC, active=DDDD; AAAA dropped
		Expect(w.Close()).To(Succeed())

		readMust := func(p string) string {
			data, err := os.ReadFile(p)
			Expect(err).NotTo(HaveOccurred())
			return string(data)
		}
		Expect(readMust(path)).To(Equal("DDDD"))
		Expect(readMust(path + ".1")).To(Equal("CCCC"))
		Expect(readMust(path + ".2")).To(Equal("BBBB"))
		_, err = os.Stat(path + ".3")
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("creates the parent directory if missing", func() {
		nested := filepath.Join(dir, "deep", "nested", "stdout.log")
		w, err := openRotatingFile(nested, 16, 1)
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write([]byte("hi"))
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())

		Expect(filepath.Join(dir, "deep", "nested", "stdout.log")).To(BeAnExistingFile())
	})

	It("appends to a pre-existing file and counts size correctly", func() {
		Expect(os.WriteFile(path, []byte("old"), 0644)).To(Succeed())

		w, err := openRotatingFile(path, 10, 1)
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write([]byte("new"))
		Expect(err).NotTo(HaveOccurred())
		// No rotate yet — total 6 < 10.
		_, err = w.Write([]byte("12345"))
		Expect(err).NotTo(HaveOccurred())
		// Now total would be 11 > 10 → rotates first.
		Expect(w.Close()).To(Succeed())

		Expect(string(mustRead(path + ".1"))).To(Equal("oldnew"))
		Expect(string(mustRead(path))).To(Equal("12345"))
	})

	It("rejects invalid sizes", func() {
		_, err := openRotatingFile(path, 0, 1)
		Expect(err).To(MatchError(ContainSubstring("maxSize")))
		_, err = openRotatingFile(path, 1, -1)
		Expect(err).To(MatchError(ContainSubstring("maxFiles")))
	})

	It("with maxFiles=0 discards old content on rotate", func() {
		w, err := openRotatingFile(path, 4, 0)
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write([]byte("AAAA"))
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write([]byte("B"))
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())

		// Active has the post-rotate write; nothing else exists.
		Expect(strings.TrimSpace(string(mustRead(path)))).To(Equal("B"))
		_, err = os.Stat(path + ".1")
		Expect(os.IsNotExist(err)).To(BeTrue())
	})
})

func mustRead(p string) []byte {
	data, err := os.ReadFile(p)
	Expect(err).NotTo(HaveOccurred())
	return data
}
