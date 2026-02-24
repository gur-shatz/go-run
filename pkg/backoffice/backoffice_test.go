package backoffice_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/backoffice"
	boclient "github.com/gur-shatz/go-run/pkg/backoffice/client"
)

var _ = Describe("Backoffice", func() {
	Describe("State Management", func() {
		BeforeEach(func() {
			// Reset singleton state between tests
			backoffice.SetReady(false)
			backoffice.SetStatus("")
			backoffice.SetMetadata(nil)
		})

		It("defaults to not ready with empty status", func() {
			info := backoffice.GetStatus()
			Expect(info.Ready).To(BeFalse())
			Expect(info.Status).To(BeEmpty())
			Expect(info.Metadata).To(BeNil())
		})

		It("tracks ready state", func() {
			backoffice.SetReady(true)
			Expect(backoffice.GetStatus().Ready).To(BeTrue())

			backoffice.SetReady(false)
			Expect(backoffice.GetStatus().Ready).To(BeFalse())
		})

		It("tracks status string", func() {
			backoffice.SetStatus("initializing")
			Expect(backoffice.GetStatus().Status).To(Equal("initializing"))
		})

		It("tracks metadata", func() {
			backoffice.SetMetadata(map[string]string{"version": "1.0"})
			info := backoffice.GetStatus()
			Expect(info.Metadata).To(HaveKeyWithValue("version", "1.0"))
		})

		It("sets individual metadata keys", func() {
			backoffice.SetMetadataKey("key1", "val1")
			backoffice.SetMetadataKey("key2", "val2")
			info := backoffice.GetStatus()
			Expect(info.Metadata).To(HaveKeyWithValue("key1", "val1"))
			Expect(info.Metadata).To(HaveKeyWithValue("key2", "val2"))
		})

		It("returns a copy of metadata", func() {
			backoffice.SetMetadata(map[string]string{"a": "b"})
			info := backoffice.GetStatus()
			info.Metadata["a"] = "modified"
			Expect(backoffice.GetStatus().Metadata).To(HaveKeyWithValue("a", "b"))
		})
	})

	Describe("ListenAndServe", func() {
		It("returns nil immediately when env var is not set", func() {
			os.Unsetenv(backoffice.EnvSockPath)
			err := backoffice.ListenAndServe(context.Background(), nil)
			Expect(err).NotTo(HaveOccurred())
		})

		It("serves /status endpoint on UDS", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			backoffice.SetReady(true)
			backoffice.SetStatus("healthy")
			backoffice.SetMetadata(map[string]string{"v": "2"})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			errCh := make(chan error, 1)
			go func() {
				errCh <- backoffice.ListenAndServe(ctx, nil)
			}()

			// Wait for socket to become available
			Eventually(func() bool {
				c := boclient.New(sockPath)
				return c.Alive(context.Background())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			// Fetch status
			c := boclient.New(sockPath)
			info, err := c.Status(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Ready).To(BeTrue())
			Expect(info.Status).To(Equal("healthy"))
			Expect(info.Metadata).To(HaveKeyWithValue("v", "2"))

			cancel()
			Eventually(errCh, 2*time.Second).Should(Receive(BeNil()))
		})

		It("serves /env endpoint with masked sensitive vars", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			os.Setenv("TEST_NORMAL_VAR", "visible")
			os.Setenv("TEST_SECRET_KEY", "should-be-masked")
			os.Setenv("TEST_DB_PASSWORD", "should-be-masked")
			defer os.Unsetenv("TEST_NORMAL_VAR")
			defer os.Unsetenv("TEST_SECRET_KEY")
			defer os.Unsetenv("TEST_DB_PASSWORD")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go backoffice.ListenAndServe(ctx, nil)

			c := boclient.New(sockPath)
			Eventually(func() bool {
				return c.Alive(context.Background())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			req, err := http.NewRequest(http.MethodGet, "http://backoffice/env", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := (&http.Client{Transport: c.Transport()}).Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var env map[string]string
			Expect(json.NewDecoder(resp.Body).Decode(&env)).To(Succeed())
			Expect(env["TEST_NORMAL_VAR"]).To(Equal("visible"))
			Expect(env["TEST_SECRET_KEY"]).To(Equal("***"))
			Expect(env["TEST_DB_PASSWORD"]).To(Equal("***"))
		})

		It("serves /info endpoint with expected keys", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go backoffice.ListenAndServe(ctx, nil)

			c := boclient.New(sockPath)
			Eventually(func() bool {
				return c.Alive(context.Background())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			req, err := http.NewRequest(http.MethodGet, "http://backoffice/info", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := (&http.Client{Transport: c.Transport()}).Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var info map[string]interface{}
			Expect(json.NewDecoder(resp.Body).Decode(&info)).To(Succeed())
			Expect(info).To(HaveKey("pid"))
			Expect(info).To(HaveKey("uptime"))
			Expect(info).To(HaveKey("go_version"))
			Expect(info).To(HaveKey("os"))
			Expect(info).To(HaveKey("arch"))
			Expect(info).To(HaveKey("num_goroutines"))
			Expect(info).To(HaveKey("num_cpu"))
			Expect(info).To(HaveKey("memory"))
		})

		It("serves / index page with HTML links", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go backoffice.ListenAndServe(ctx, nil)

			c := boclient.New(sockPath)
			Eventually(func() bool {
				return c.Alive(context.Background())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			req, err := http.NewRequest(http.MethodGet, "http://backoffice/", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := (&http.Client{Transport: c.Transport()}).Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("<html>"))
			Expect(string(body)).To(ContainSubstring(`href="status"`))
			Expect(string(body)).To(ContainSubstring(`href="env"`))
			Expect(string(body)).To(ContainSubstring(`href="info"`))
			Expect(string(body)).To(ContainSubstring(`href="debug/pprof/"`))
		})

		It("serves /debug/pprof/ endpoint", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go backoffice.ListenAndServe(ctx, nil)

			c := boclient.New(sockPath)
			Eventually(func() bool {
				return c.Alive(context.Background())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			req, err := http.NewRequest(http.MethodGet, "http://backoffice/debug/pprof/", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := (&http.Client{Transport: c.Transport()}).Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
		})

		It("serves user router alongside /status", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			userRouter := chi.NewRouter()
			userRouter.Get("/custom", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"hello": "world"})
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go backoffice.ListenAndServe(ctx, userRouter)

			// Wait for socket
			c := boclient.New(sockPath)
			Eventually(func() bool {
				return c.Alive(context.Background())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			// Fetch custom endpoint
			req, err := http.NewRequest(http.MethodGet, "http://backoffice/custom", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := (&http.Client{Transport: c.Transport()}).Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var body map[string]string
			Expect(json.NewDecoder(resp.Body).Decode(&body)).To(Succeed())
			Expect(body).To(HaveKeyWithValue("hello", "world"))
		})
	})
})

var _ = Describe("Client", func() {
	It("reports not alive for nonexistent socket", func() {
		c := boclient.New("/tmp/nonexistent-gorun-test.sock")
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		Expect(c.Alive(ctx)).To(BeFalse())
	})

	It("returns error for status on nonexistent socket", func() {
		c := boclient.New("/tmp/nonexistent-gorun-test.sock")
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_, err := c.Status(ctx)
		Expect(err).To(HaveOccurred())
	})

	It("exposes sock path", func() {
		c := boclient.New("/some/path.sock")
		Expect(c.SockPath()).To(Equal("/some/path.sock"))
	})

	It("exposes transport", func() {
		c := boclient.New("/some/path.sock")
		Expect(c.Transport()).NotTo(BeNil())
	})
})
