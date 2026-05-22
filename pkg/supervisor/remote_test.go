package supervisor_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

type fakeOrigin struct {
	mu       sync.Mutex
	files    map[string]string
	missing  map[string]bool
	requests []string
}

func newFakeOrigin() *fakeOrigin {
	return &fakeOrigin{
		files:   make(map[string]string),
		missing: make(map[string]bool),
	}
}

func (this *fakeOrigin) serve() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		this.mu.Lock()
		this.requests = append(this.requests, r.URL.Path)
		this.mu.Unlock()

		if this.missing[r.URL.Path] {
			http.NotFound(w, r)
			return
		}
		body, ok := this.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}
}

var _ = Describe("RemoteClient", func() {
	var (
		origin *fakeOrigin
		server *httptest.Server
		client *supervisor.RemoteClient
	)

	BeforeEach(func() {
		origin = newFakeOrigin()
		server = httptest.NewServer(origin.serve())
		client = supervisor.NewRemoteClient("")
		client.SetPlatform("linux", "amd64")
	})

	AfterEach(func() { server.Close() })

	Describe("ResolveVersion", func() {
		It("returns the trimmed version body on a direct hit", func() {
			origin.files["/api/versions/required.txt"] = "1.4.2\n"

			v, err := client.ResolveVersion(context.Background(), server.URL, "api", "required.txt")
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal("1.4.2"))
		})

		It("follows an @redirect to a sibling file", func() {
			origin.files["/api/versions/required.txt"] = "@required2.txt\n"
			origin.files["/api/versions/required2.txt"] = "1.5.0"

			v, err := client.ResolveVersion(context.Background(), server.URL, "api", "required.txt")
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal("1.5.0"))
		})

		It("returns ErrRedirectLoop when the chain cycles", func() {
			origin.files["/api/versions/required.txt"] = "@required2.txt"
			origin.files["/api/versions/required2.txt"] = "@required.txt"

			_, err := client.ResolveVersion(context.Background(), server.URL, "api", "required.txt")
			Expect(err).To(MatchError(supervisor.ErrRedirectLoop))
		})

		It("returns ErrRedirectLoop when the chain exceeds the cap", func() {
			origin.files["/api/versions/required.txt"] = "@a.txt"
			origin.files["/api/versions/a.txt"] = "@b.txt"
			origin.files["/api/versions/b.txt"] = "@c.txt"
			origin.files["/api/versions/c.txt"] = "@d.txt"
			origin.files["/api/versions/d.txt"] = "@e.txt"
			origin.files["/api/versions/e.txt"] = "@f.txt"
			origin.files["/api/versions/f.txt"] = "1.0.0"

			_, err := client.ResolveVersion(context.Background(), server.URL, "api", "required.txt")
			Expect(err).To(MatchError(supervisor.ErrRedirectLoop))
		})
	})

	Describe("ImageURLs", func() {
		It("builds the platform-specific archive and signature URLs", func() {
			archive, sig, err := client.ImageURLs(server.URL, "api", "1.4.2")
			Expect(err).NotTo(HaveOccurred())
			Expect(archive).To(HaveSuffix("/api/images/1.4.2_linux_amd64.tar.gz"))
			Expect(sig).To(Equal(archive + ".sig"))
		})
	})

	Describe("DownloadImage", func() {
		It("fetches both archive and signature", func() {
			origin.files["/api/images/1.4.2_linux_amd64.tar.gz"] = "ARCHIVE"
			origin.files["/api/images/1.4.2_linux_amd64.tar.gz.sig"] = "SIG"

			archive, sig, err := client.DownloadImage(context.Background(), server.URL, "api", "1.4.2")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(archive)).To(Equal("ARCHIVE"))
			Expect(string(sig)).To(Equal("SIG"))
		})

		It("returns ErrImageNotFoundForPlatform when the archive is missing", func() {
			origin.files["/api/images/1.4.2_linux_amd64.tar.gz.sig"] = "SIG"
			_, _, err := client.DownloadImage(context.Background(), server.URL, "api", "1.4.2")
			Expect(errors.Is(err, supervisor.ErrImageNotFoundForPlatform)).To(BeTrue())
		})

		It("returns ErrImageNotFoundForPlatform when only the signature is missing", func() {
			origin.files["/api/images/1.4.2_linux_amd64.tar.gz"] = "ARCHIVE"
			_, _, err := client.DownloadImage(context.Background(), server.URL, "api", "1.4.2")
			Expect(errors.Is(err, supervisor.ErrImageNotFoundForPlatform)).To(BeTrue())
		})
	})
})
