package supervisor_test

import (
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("KillSocket", func() {
	var sockPath string

	BeforeEach(func() {
		sockPath = filepath.Join(GinkgoT().TempDir(), "kill.sock")
	})

	It("fires the callback exactly once when a peer writes KILL\\n", func() {
		var fired atomic.Int32
		ks, err := supervisor.ListenKillSocket(sockPath, func() { fired.Add(1) })
		Expect(err).NotTo(HaveOccurred())
		defer ks.Close()

		conn, err := net.Dial("unix", sockPath)
		Expect(err).NotTo(HaveOccurred())
		_, err = conn.Write([]byte("KILL\n"))
		Expect(err).NotTo(HaveOccurred())
		conn.Close()

		Eventually(func() int32 { return fired.Load() }).Should(Equal(int32(1)))
	})

	It("ignores connections that do not write KILL\\n", func() {
		var fired atomic.Int32
		ks, err := supervisor.ListenKillSocket(sockPath, func() { fired.Add(1) })
		Expect(err).NotTo(HaveOccurred())
		defer ks.Close()

		conn, err := net.Dial("unix", sockPath)
		Expect(err).NotTo(HaveOccurred())
		_, err = conn.Write([]byte("nope\n"))
		Expect(err).NotTo(HaveOccurred())
		conn.Close()

		Consistently(func() int32 { return fired.Load() }, 100*time.Millisecond).Should(BeZero())
	})

	It("does not fire twice when two KILLs arrive", func() {
		var fired atomic.Int32
		ks, err := supervisor.ListenKillSocket(sockPath, func() { fired.Add(1) })
		Expect(err).NotTo(HaveOccurred())
		defer ks.Close()

		for range 3 {
			conn, err := net.Dial("unix", sockPath)
			Expect(err).NotTo(HaveOccurred())
			_, _ = conn.Write([]byte("KILL\n"))
			conn.Close()
		}
		Eventually(func() int32 { return fired.Load() }).Should(Equal(int32(1)))
		Consistently(func() int32 { return fired.Load() }, 100*time.Millisecond).Should(Equal(int32(1)))
	})

	It("creates the socket with 0600 permissions", func() {
		ks, err := supervisor.ListenKillSocket(sockPath, func() {})
		Expect(err).NotTo(HaveOccurred())
		defer ks.Close()

		info, err := os.Stat(sockPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0600)))
	})

	It("removes the socket file on Close", func() {
		ks, err := supervisor.ListenKillSocket(sockPath, func() {})
		Expect(err).NotTo(HaveOccurred())
		Expect(ks.Close()).To(Succeed())

		_, err = os.Stat(sockPath)
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("recovers from a leftover socket file at the same path", func() {
		Expect(os.WriteFile(sockPath, []byte("leftover"), 0600)).To(Succeed())

		ks, err := supervisor.ListenKillSocket(sockPath, func() {})
		Expect(err).NotTo(HaveOccurred())
		defer ks.Close()
	})
})
