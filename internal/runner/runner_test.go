package runner_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/internal/runner"
)

var _ = Describe("Runner", func() {
	var (
		tmpDir string
		origDir string
		stdout *bytes.Buffer
		stderr *bytes.Buffer
	)

	BeforeEach(func() {
		var err error
		origDir, err = os.Getwd()
		Expect(err).NotTo(HaveOccurred())

		tmpDir = GinkgoT().TempDir()
		Expect(os.Chdir(tmpDir)).To(Succeed())

		stdout = &bytes.Buffer{}
		stderr = &bytes.Buffer{}
	})

	AfterEach(func() {
		Expect(os.Chdir(origDir)).To(Succeed())
	})

	writeMainGo := func(code string) {
		err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(code), 0644)
		Expect(err).NotTo(HaveOccurred())
	}

	writeGoMod := func() {
		err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module testapp\n\ngo 1.21\n"), 0644)
		Expect(err).NotTo(HaveOccurred())
	}

	testLogger := log.New("[test]", false)

	newRunner := func() *runner.Runner {
		return runner.New(context.Background(), tmpDir, ".", nil, nil, nil, "testapp", stdout, stderr, nil, nil, testLogger)
	}

	Describe("Build", func() {
		It("compiles a valid Go program", func() {
			writeGoMod()
			writeMainGo(`package main
import "fmt"
func main() { fmt.Println("hello") }
`)
			r := newRunner()
			defer r.Cleanup()

			dur, err := r.Build()
			Expect(err).NotTo(HaveOccurred())
			Expect(dur).To(BeNumerically(">", 0))
			Expect(r.BinPath()).NotTo(BeEmpty())

			_, err = os.Stat(r.BinPath())
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns error for invalid Go code", func() {
			writeGoMod()
			writeMainGo(`package main
func main() { this is invalid }
`)
			r := newRunner()
			defer r.Cleanup()

			_, err := r.Build()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("build failed"))
		})
	})

	Describe("Start", func() {
		It("runs the built binary and sets Running/PID", func() {
			writeGoMod()
			writeMainGo(`package main
import "time"
func main() { time.Sleep(10 * time.Second) }
`)
			r := newRunner()
			defer r.Cleanup()

			_, err := r.Build()
			Expect(err).NotTo(HaveOccurred())

			err = r.Start()
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Running()).To(BeTrue())
			Expect(r.PID()).To(BeNumerically(">", 0))
		})

		It("returns error when no binary is built", func() {
			r := newRunner()
			err := r.Start()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no binary built"))
		})
	})

	Describe("Stop", func() {
		It("terminates the process", func() {
			writeGoMod()
			writeMainGo(`package main
import "time"
func main() { time.Sleep(10 * time.Second) }
`)
			r := newRunner()
			defer r.Cleanup()

			_, err := r.Build()
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Start()).To(Succeed())
			Expect(r.Running()).To(BeTrue())

			Expect(r.Stop()).To(Succeed())
			Eventually(r.Running, 2*time.Second).Should(BeFalse())
		})

		It("is safe to call when not running", func() {
			r := newRunner()
			Expect(r.Stop()).To(Succeed())
		})
	})

	Describe("Exited", func() {
		It("fires when process exits on its own", func() {
			writeGoMod()
			writeMainGo(`package main
func main() {}
`)
			r := newRunner()
			defer r.Cleanup()

			_, err := r.Build()
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Start()).To(Succeed())

			var info runner.ExitInfo
			Eventually(r.Exited(), 5*time.Second).Should(Receive(&info))
			Expect(info.ExitCode).To(Equal(0))
		})

		It("does not fire when Stop is used", func() {
			writeGoMod()
			writeMainGo(`package main
import "time"
func main() { time.Sleep(10 * time.Second) }
`)
			r := newRunner()
			defer r.Cleanup()

			_, err := r.Build()
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Start()).To(Succeed())
			Expect(r.Stop()).To(Succeed())

			Consistently(r.Exited(), 500*time.Millisecond).ShouldNot(Receive())
		})
	})

	Describe("Restart", func() {
		It("builds, stops old, starts new", func() {
			writeGoMod()
			writeMainGo(`package main
import "time"
func main() { time.Sleep(10 * time.Second) }
`)
			r := newRunner()
			defer r.Cleanup()

			_, err := r.Build()
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Start()).To(Succeed())
			oldPID := r.PID()

			dur, err := r.Restart()
			Expect(err).NotTo(HaveOccurred())
			Expect(dur).To(BeNumerically(">", 0))
			Expect(r.Running()).To(BeTrue())
			Expect(r.PID()).NotTo(Equal(oldPID))
		})
	})

	Describe("Cleanup", func() {
		It("stops process and removes temp binary", func() {
			writeGoMod()
			writeMainGo(`package main
import "time"
func main() { time.Sleep(10 * time.Second) }
`)
			r := newRunner()

			_, err := r.Build()
			Expect(err).NotTo(HaveOccurred())
			binPath := r.BinPath()
			Expect(r.Start()).To(Succeed())

			r.Cleanup()
			Eventually(r.Running, 2*time.Second).Should(BeFalse())
			_, err = os.Stat(binPath)
			Expect(os.IsNotExist(err)).To(BeTrue())
		})
	})

	Describe("CmdLine", func() {
		It("returns correct string", func() {
			r := runner.New(context.Background(), tmpDir, "./cmd/app", nil, []string{"-port", "8080"}, nil, "app", stdout, stderr, nil, nil, testLogger)
			Expect(r.CmdLine()).To(Equal("./cmd/app -port 8080"))
		})

		It("returns just target when no args", func() {
			r := runner.New(context.Background(), tmpDir, "./cmd/app", nil, nil, nil, "app", stdout, stderr, nil, nil, testLogger)
			Expect(r.CmdLine()).To(Equal("./cmd/app"))
		})
	})
})
