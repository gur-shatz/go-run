package supervisor_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

func mkVersionFolder(versionsDir, name string, mtime time.Time) string {
	dir := filepath.Join(versionsDir, name)
	Expect(os.MkdirAll(dir, 0755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0644)).To(Succeed())
	Expect(os.Chtimes(dir, mtime, mtime)).To(Succeed())
	return dir
}

var _ = Describe("CleanOrphanVersions", func() {
	var (
		paths supervisor.ComponentPaths
	)

	BeforeEach(func() {
		stateDir := GinkgoT().TempDir()
		paths = supervisor.NewPaths(stateDir).Component("api")
		Expect(paths.EnsureDirs()).To(Succeed())
	})

	It("keeps versions referenced by stable/current/rejects", func() {
		mkVersionFolder(paths.Versions(), "stable-v", time.Now().Add(-3*time.Hour))
		mkVersionFolder(paths.Versions(), "current-v", time.Now().Add(-2*time.Hour))
		mkVersionFolder(paths.Versions(), "rejected-v", time.Now().Add(-time.Hour))
		mkVersionFolder(paths.Versions(), "orphan-v", time.Now())

		Expect(paths.WriteStable("stable-v")).To(Succeed())
		Expect(paths.WriteCurrent("current-v")).To(Succeed())
		Expect(paths.AppendReject("rejected-v")).To(Succeed())

		_, err := supervisor.CleanOrphanVersions(paths, 0)
		Expect(err).NotTo(HaveOccurred())

		for _, name := range []string{"stable-v", "current-v", "rejected-v"} {
			_, err := os.Stat(filepath.Join(paths.Versions(), name))
			Expect(err).NotTo(HaveOccurred(), "expected %s to be kept", name)
		}
		_, err = os.Stat(filepath.Join(paths.Versions(), "orphan-v"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("retains the newest N orphans", func() {
		now := time.Now()
		mkVersionFolder(paths.Versions(), "old-1", now.Add(-3*time.Hour))
		mkVersionFolder(paths.Versions(), "old-2", now.Add(-2*time.Hour))
		mkVersionFolder(paths.Versions(), "new-1", now.Add(-time.Minute))
		mkVersionFolder(paths.Versions(), "new-2", now)

		res, err := supervisor.CleanOrphanVersions(paths, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Deleted).To(ConsistOf("old-1", "old-2"))

		_, err = os.Stat(filepath.Join(paths.Versions(), "new-1"))
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(filepath.Join(paths.Versions(), "new-2"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("is a noop when there are no orphans", func() {
		mkVersionFolder(paths.Versions(), "current-v", time.Now())
		Expect(paths.WriteCurrent("current-v")).To(Succeed())

		res, err := supervisor.CleanOrphanVersions(paths, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Deleted).To(BeEmpty())
	})

	It("handles a missing versions directory", func() {
		nonexistent := supervisor.NewPaths(GinkgoT().TempDir()).Component("never-existed")
		_, err := supervisor.CleanOrphanVersions(nonexistent, 2)
		Expect(err).NotTo(HaveOccurred())
	})
})
