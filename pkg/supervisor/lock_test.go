package supervisor_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("FileLock", func() {
	var lockPath string

	BeforeEach(func() {
		lockPath = filepath.Join(GinkgoT().TempDir(), "supervisor.lock")
	})

	It("acquires the lock when no one holds it", func() {
		lock, err := supervisor.AcquireLock(lockPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(lock).NotTo(BeNil())
		Expect(lock.Release()).To(Succeed())
	})

	It("fails fast when another holder has the lock", func() {
		first, err := supervisor.AcquireLock(lockPath)
		Expect(err).NotTo(HaveOccurred())
		defer first.Release()

		_, err = supervisor.AcquireLock(lockPath)
		Expect(err).To(MatchError(supervisor.ErrAlreadyRunning))
	})

	It("lets a second process acquire after the first releases", func() {
		first, err := supervisor.AcquireLock(lockPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Release()).To(Succeed())

		second, err := supervisor.AcquireLock(lockPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Release()).To(Succeed())
	})
})
