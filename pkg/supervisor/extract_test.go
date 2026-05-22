package supervisor_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

type tarEntry struct {
	Name     string
	Mode     int64
	Body     string
	Typeflag byte
	Linkname string
}

func makeTarGz(entries []tarEntry) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		mode := e.Mode
		if mode == 0 {
			mode = 0644
		}
		hdr := &tar.Header{
			Name:     e.Name,
			Mode:     mode,
			Size:     int64(len(e.Body)),
			Typeflag: e.Typeflag,
			Linkname: e.Linkname,
			ModTime:  time.Unix(0, 0),
		}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Typeflag == tar.TypeDir {
			hdr.Size = 0
		}
		Expect(tw.WriteHeader(hdr)).To(Succeed())
		if hdr.Typeflag == tar.TypeReg {
			_, err := io.WriteString(tw, e.Body)
			Expect(err).NotTo(HaveOccurred())
		}
	}
	Expect(tw.Close()).To(Succeed())
	Expect(gz.Close()).To(Succeed())
	return buf.Bytes()
}

var _ = Describe("ExtractTarGz", func() {
	var destDir string

	BeforeEach(func() {
		destDir = filepath.Join(GinkgoT().TempDir(), "out")
	})

	It("extracts regular files with their mode bits", func() {
		archive := makeTarGz([]tarEntry{
			{Name: "bin/", Typeflag: tar.TypeDir, Mode: 0755},
			{Name: "bin/api", Body: "binary contents", Mode: 0755},
			{Name: "README", Body: "hello", Mode: 0644},
		})

		Expect(supervisor.ExtractTarGz(bytes.NewReader(archive), destDir)).To(Succeed())

		data, err := os.ReadFile(filepath.Join(destDir, "bin", "api"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("binary contents"))

		info, err := os.Stat(filepath.Join(destDir, "bin", "api"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0755)))
	})

	It("rejects an entry with an absolute path", func() {
		archive := makeTarGz([]tarEntry{
			{Name: "/etc/passwd", Body: "haha", Mode: 0644},
		})
		err := supervisor.ExtractTarGz(bytes.NewReader(archive), destDir)
		Expect(err).To(MatchError(ContainSubstring("absolute")))
	})

	It("rejects an entry with a .. component", func() {
		archive := makeTarGz([]tarEntry{
			{Name: "../etc/passwd", Body: "haha", Mode: 0644},
		})
		err := supervisor.ExtractTarGz(bytes.NewReader(archive), destDir)
		Expect(err).To(MatchError(ContainSubstring("escapes destination")))
	})

	It("rejects a symlink whose link name escapes", func() {
		archive := makeTarGz([]tarEntry{
			{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd"},
		})
		err := supervisor.ExtractTarGz(bytes.NewReader(archive), destDir)
		Expect(err).To(MatchError(ContainSubstring("escapes destination")))
	})

	It("creates intermediate directories implicitly", func() {
		archive := makeTarGz([]tarEntry{
			{Name: "deeply/nested/file", Body: "x"},
		})
		Expect(supervisor.ExtractTarGz(bytes.NewReader(archive), destDir)).To(Succeed())

		_, err := os.Stat(filepath.Join(destDir, "deeply", "nested", "file"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("returns a useful error on a non-gzip input", func() {
		err := supervisor.ExtractTarGz(bytes.NewReader([]byte("not gzip")), destDir)
		Expect(err).To(MatchError(ContainSubstring("gzip reader")))
	})
})
