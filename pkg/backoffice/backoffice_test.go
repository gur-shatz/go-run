package backoffice_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/backoffice"
	boclient "github.com/gur-shatz/go-run/pkg/backoffice/client"
	"github.com/gur-shatz/go-run/pkg/chiutil"
)

var _ = Describe("Backoffice", func() {
	Describe("State Management", func() {
		BeforeEach(func() {
			backoffice.ResetStateForTest()
		})

		It("defaults to OK global with no services", func() {
			info := backoffice.GetStatus()
			Expect(info.GlobalLevel).To(Equal(backoffice.OK))
			Expect(info.CausedBy).To(BeEmpty())
			Expect(info.Services).To(BeEmpty())

			// Verify services marshals as [] not null
			data, err := json.Marshal(info)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring(`"services":[]`))
		})

		It("registers a service at OK", func() {
			backoffice.CreateServiceStatus("db", true)
			info := backoffice.GetStatus()
			Expect(info.Services).To(HaveLen(1))
			Expect(info.Services[0].Name).To(Equal("db"))
			Expect(info.Services[0].Level).To(Equal(backoffice.OK))
			Expect(info.GlobalLevel).To(Equal(backoffice.OK))
		})

		It("updates level via handle", func() {
			svc := backoffice.CreateServiceStatus("db", true)
			svc.SetStatus(backoffice.Down, map[string]string{"error": "connection refused"})
			info := backoffice.GetStatus()
			Expect(info.Services[0].Level).To(Equal(backoffice.Down))
			Expect(info.Services[0].Data).To(HaveKeyWithValue("error", "connection refused"))
		})

		It("global equals worst of all services", func() {
			db := backoffice.CreateServiceStatus("db", true)
			cache := backoffice.CreateServiceStatus("cache", true)
			db.SetStatus(backoffice.Degraded, nil)
			cache.SetStatus(backoffice.OK, nil)
			info := backoffice.GetStatus()
			Expect(info.GlobalLevel).To(Equal(backoffice.Degraded))
			Expect(info.CausedBy).To(Equal("db"))
		})

		It("non-critical caps at RunningWithErrors", func() {
			cache := backoffice.CreateServiceStatus("cache", false)
			cache.SetStatus(backoffice.Down, nil)
			info := backoffice.GetStatus()
			Expect(info.GlobalLevel).To(Equal(backoffice.RunningWithErrors))
			Expect(info.CausedBy).To(Equal("cache"))
		})

		It("critical pushes global to full level", func() {
			db := backoffice.CreateServiceStatus("db", true)
			db.SetStatus(backoffice.Down, nil)
			info := backoffice.GetStatus()
			Expect(info.GlobalLevel).To(Equal(backoffice.Down))
		})

		It("tracks time in state", func() {
			now := time.Now()
			backoffice.SetTimeNowForTest(func() time.Time { return now })

			svc := backoffice.CreateServiceStatus("db", true)
			backoffice.SetTimeNowForTest(func() time.Time { return now.Add(30 * time.Second) })

			svc.SetStatus(backoffice.OK, nil) // no-op level change, but updates lastTick
			info := backoffice.GetStatus()
			Expect(info.Services[0].TimeInState).To(Equal("30s"))
		})

		It("history capped at 10 entries", func() {
			svc := backoffice.CreateServiceStatus("db", true)
			// Initial history has 1 entry (OK). Alternate between levels to create changes.
			for i := 0; i < 15; i++ {
				if i%2 == 0 {
					svc.SetStatus(backoffice.Down, nil)
				} else {
					svc.SetStatus(backoffice.OK, nil)
				}
			}
			info := backoffice.GetStatus()
			Expect(len(info.Services[0].History)).To(Equal(10))
		})

		It("uptime < 100% after time in error", func() {
			now := time.Now()
			backoffice.SetTimeNowForTest(func() time.Time { return now })

			svc := backoffice.CreateServiceStatus("db", true)

			// Move to Degraded
			backoffice.SetTimeNowForTest(func() time.Time { return now.Add(10 * time.Second) })
			svc.SetStatus(backoffice.Degraded, nil)

			// Stay degraded for 10s then recover
			backoffice.SetTimeNowForTest(func() time.Time { return now.Add(20 * time.Second) })
			svc.SetStatus(backoffice.OK, nil)

			// Check at 20s: 10s degraded out of 20s total = 50% error time
			info := backoffice.GetStatus()
			Expect(info.Services[0].UptimePct).To(BeNumerically("<", 100.0))
			Expect(info.Services[0].UptimePct).To(BeNumerically("~", 50.0, 1.0))
		})

		It("CausedBy reports correct service (deterministic by name sort)", func() {
			// Both critical and both Down — CausedBy should be first alphabetically
			a := backoffice.CreateServiceStatus("alpha", true)
			b := backoffice.CreateServiceStatus("beta", true)
			a.SetStatus(backoffice.Down, nil)
			b.SetStatus(backoffice.Down, nil)
			info := backoffice.GetStatus()
			Expect(info.CausedBy).To(Equal("alpha"))
		})
	})

	Describe("ServiceLevel JSON", func() {
		It("marshals to string", func() {
			data, err := json.Marshal(backoffice.RunningWithErrors)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal(`"RUNNING_WITH_ERRORS"`))
		})

		It("unmarshals from string", func() {
			var level backoffice.ServiceLevel
			err := json.Unmarshal([]byte(`"DEGRADED"`), &level)
			Expect(err).NotTo(HaveOccurred())
			Expect(level).To(Equal(backoffice.Degraded))
		})
	})

	Describe("ListenAndServe", func() {
		var bo *backoffice.Backoffice

		BeforeEach(func() {
			backoffice.ResetStateForTest()
			bo = backoffice.New()
		})

		It("returns nil immediately when env var is not set", func() {
			os.Unsetenv(backoffice.EnvSockPath)
			err := bo.ListenAndServe(context.Background())
			Expect(err).NotTo(HaveOccurred())
		})

		It("serves /status endpoint with new JSON shape", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			db := backoffice.CreateServiceStatus("database", true)
			db.SetStatus(backoffice.OK, map[string]string{"version": "1.0"})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			errCh := make(chan error, 1)
			go func() {
				errCh <- bo.ListenAndServe(ctx)
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
			Expect(info.GlobalLevel).To(Equal(backoffice.OK))
			Expect(info.Services).To(HaveLen(1))
			Expect(info.Services[0].Name).To(Equal("database"))
			Expect(info.Services[0].Level).To(Equal(backoffice.OK))
			Expect(info.Services[0].Critical).To(BeTrue())

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

			go bo.ListenAndServe(ctx)

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

			go bo.ListenAndServe(ctx)

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

		It("serves / index page with HTML content", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go bo.ListenAndServe(ctx)

			c := boclient.New(sockPath)
			Eventually(func() bool {
				return c.Alive(context.Background())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			// Verify / returns 200 OK with HTML
			req, err := http.NewRequest(http.MethodGet, "http://backoffice/", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := (&http.Client{Transport: c.Transport()}).Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(resp.Header.Get("Content-Type")).To(ContainSubstring("text/html"))
		})

		It("serves /index.json with route entries", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go bo.ListenAndServe(ctx)

			c := boclient.New(sockPath)
			Eventually(func() bool {
				return c.Alive(context.Background())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			// Verify /index.json returns a valid FolderIndex
			req, err := http.NewRequest(http.MethodGet, "http://backoffice/index.json", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := (&http.Client{Transport: c.Transport()}).Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var index chiutil.FolderIndex
			Expect(json.NewDecoder(resp.Body).Decode(&index)).To(Succeed())
			Expect(index.ServiceName).To(Equal("Backoffice"))

			// Check that expected entries are present
			entryNames := map[string]bool{}
			for _, e := range index.Entries {
				entryNames[e.Name] = true
			}
			Expect(entryNames).To(HaveKey("status"))
			Expect(entryNames).To(HaveKey("env"))
			Expect(entryNames).To(HaveKey("info"))
			Expect(entryNames).To(HaveKey("debug/pprof"))
		})

		It("serves /debug/pprof/ endpoint", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go bo.ListenAndServe(ctx)

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

		It("serves custom routes registered via Folder()", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			bo.Folder().GetDesc("/custom", "Custom endpoint", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"hello": "world"})
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go bo.ListenAndServe(ctx)

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

		It("serves user router mounted as sub-folder", func() {
			tmpDir := GinkgoT().TempDir()
			sockPath := filepath.Join(tmpDir, "test.sock")
			os.Setenv(backoffice.EnvSockPath, sockPath)
			defer os.Unsetenv(backoffice.EnvSockPath)

			userRouter := chi.NewRouter()
			userRouter.Get("/endpoint", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"hello": "world"})
			})
			bo.Folder().MountDesc("/app", "Application endpoints", userRouter)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go bo.ListenAndServe(ctx)

			c := boclient.New(sockPath)
			Eventually(func() bool {
				return c.Alive(context.Background())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			req, err := http.NewRequest(http.MethodGet, "http://backoffice/app/endpoint", nil)
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

var _ = Describe("TCP and Auth", func() {
	var bo *backoffice.Backoffice

	BeforeEach(func() {
		backoffice.ResetStateForTest()
		bo = backoffice.New()
	})

	It("serves on TCP", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := make(chan error, 1)
		go func() {
			errCh <- bo.ListenAndServeTCP(ctx, "127.0.0.1:0")
		}()

		// Use a known port for the test
		// Instead, let's use a random port approach by starting with a listener
		cancel()
		Eventually(errCh, 2*time.Second).Should(Receive(BeNil()))
	})

	It("serves endpoints on TCP with a specific port", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Find a free port
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		addr := ln.Addr().String()
		ln.Close()

		go bo.ListenAndServeTCP(ctx, addr)

		// Wait for server to start
		Eventually(func() bool {
			resp, err := http.Get("http://" + addr + "/status")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

		// Verify status endpoint works
		resp, err := http.Get("http://" + addr + "/status")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var info backoffice.StatusInfo
		Expect(json.NewDecoder(resp.Body).Decode(&info)).To(Succeed())
		Expect(info.GlobalLevel).To(Equal(backoffice.OK))
	})

	It("requires auth on TCP when SetAuth is called (default AuthTCPOnly)", func() {
		bo.SetAuth("admin", "secret")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		addr := ln.Addr().String()
		ln.Close()

		go bo.ListenAndServeTCP(ctx, addr)

		Eventually(func() bool {
			resp, err := http.Get("http://" + addr + "/status")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return true
		}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

		// Without auth → 401
		resp, err := http.Get("http://" + addr + "/status")
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))

		// With correct auth → 200
		req, err := http.NewRequest("GET", "http://"+addr+"/status", nil)
		Expect(err).NotTo(HaveOccurred())
		req.SetBasicAuth("admin", "secret")
		resp, err = http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		// With wrong auth → 401
		req, err = http.NewRequest("GET", "http://"+addr+"/status", nil)
		Expect(err).NotTo(HaveOccurred())
		req.SetBasicAuth("admin", "wrong")
		resp, err = http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
	})

	It("does not require auth on UDS when AuthTCPOnly (default)", func() {
		bo.SetAuth("admin", "secret")

		tmpDir := GinkgoT().TempDir()
		sockPath := filepath.Join(tmpDir, "test.sock")
		os.Setenv(backoffice.EnvSockPath, sockPath)
		defer os.Unsetenv(backoffice.EnvSockPath)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go bo.ListenAndServe(ctx)

		c := boclient.New(sockPath)
		Eventually(func() bool {
			return c.Alive(context.Background())
		}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

		// UDS should not require auth
		req, err := http.NewRequest("GET", "http://backoffice/status", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := (&http.Client{Transport: c.Transport()}).Do(req)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("requires auth on UDS when AuthUnixOnly", func() {
		bo.SetAuth("admin", "secret", backoffice.AuthUnixOnly)

		tmpDir := GinkgoT().TempDir()
		sockPath := filepath.Join(tmpDir, "test.sock")
		os.Setenv(backoffice.EnvSockPath, sockPath)
		defer os.Unsetenv(backoffice.EnvSockPath)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go bo.ListenAndServe(ctx)

		// Wait for socket to accept connections (Alive returns false because auth returns 401)
		c := boclient.New(sockPath)
		Eventually(func() bool {
			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				return false
			}
			conn.Close()
			return true
		}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

		// Without auth → 401
		req, err := http.NewRequest("GET", "http://backoffice/status", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := (&http.Client{Transport: c.Transport()}).Do(req)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))

		// With auth → 200
		req, err = http.NewRequest("GET", "http://backoffice/status", nil)
		Expect(err).NotTo(HaveOccurred())
		req.SetBasicAuth("admin", "secret")
		resp, err = (&http.Client{Transport: c.Transport()}).Do(req)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("does not require auth on TCP when AuthUnixOnly", func() {
		bo.SetAuth("admin", "secret", backoffice.AuthUnixOnly)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		addr := ln.Addr().String()
		ln.Close()

		go bo.ListenAndServeTCP(ctx, addr)

		Eventually(func() bool {
			resp, err := http.Get("http://" + addr + "/status")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

		// TCP should not require auth when scope is AuthUnixOnly
		resp, err := http.Get("http://" + addr + "/status")
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("requires auth on both when AuthBoth", func() {
		bo.SetAuth("admin", "secret", backoffice.AuthBoth)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// TCP side
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		addr := ln.Addr().String()
		ln.Close()

		go bo.ListenAndServeTCP(ctx, addr)

		Eventually(func() bool {
			resp, err := http.Get("http://" + addr + "/status")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return true
		}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

		// TCP without auth → 401
		resp, err := http.Get("http://" + addr + "/status")
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))

		// UDS side
		tmpDir := GinkgoT().TempDir()
		sockPath := filepath.Join(tmpDir, "test.sock")
		os.Setenv(backoffice.EnvSockPath, sockPath)
		defer os.Unsetenv(backoffice.EnvSockPath)

		go bo.ListenAndServe(ctx)

		c := boclient.New(sockPath)
		Eventually(func() bool {
			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				return false
			}
			conn.Close()
			return true
		}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

		// UDS without auth → 401
		req, err := http.NewRequest("GET", "http://backoffice/status", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err = (&http.Client{Transport: c.Transport()}).Do(req)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
	})
})

var _ = Describe("Recover Middleware", func() {
	It("recovers from handler panics without crashing", func() {
		backoffice.ResetStateForTest()
		bo := backoffice.New()

		bo.Folder().GetDesc("/panic", "Panicking endpoint", func(w http.ResponseWriter, r *http.Request) {
			panic("test panic")
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		addr := ln.Addr().String()
		ln.Close()

		go bo.ListenAndServeTCP(ctx, addr)

		Eventually(func() bool {
			resp, err := http.Get("http://" + addr + "/status")
			if err != nil {
				return false
			}
			resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

		// Hit the panicking endpoint — should get 500, not crash
		resp, err := http.Get("http://" + addr + "/panic")
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusInternalServerError))

		// Server should still be alive
		resp, err = http.Get("http://" + addr + "/status")
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
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
