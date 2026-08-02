package supervisor_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/statekit"

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

		_, err := supervisor.CleanOrphanVersions(paths, supervisor.VersionGCPolicy{})
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

		res, err := supervisor.CleanOrphanVersions(paths, supervisor.VersionGCPolicy{Retain: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Deleted).To(ConsistOf("old-1", "old-2"))

		_, err = os.Stat(filepath.Join(paths.Versions(), "new-1"))
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(filepath.Join(paths.Versions(), "new-2"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("keeps orphans younger than the minimum age", func() {
		now := time.Now()
		mkVersionFolder(paths.Versions(), "ancient", now.Add(-8*24*time.Hour))
		mkVersionFolder(paths.Versions(), "week-old", now.Add(-6*24*time.Hour))
		mkVersionFolder(paths.Versions(), "fresh", now.Add(-time.Minute))

		res, err := supervisor.CleanOrphanVersions(paths, supervisor.VersionGCPolicy{MinAge: 7 * 24 * time.Hour})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Deleted).To(ConsistOf("ancient"))
		Expect(res.Kept).To(ConsistOf("week-old", "fresh"))

		for _, name := range []string{"week-old", "fresh"} {
			_, err := os.Stat(filepath.Join(paths.Versions(), name))
			Expect(err).NotTo(HaveOccurred(), "expected %s to be kept", name)
		}
	})

	It("requires both the retention slot and the age floor to be exceeded", func() {
		now := time.Now()
		mkVersionFolder(paths.Versions(), "old-1", now.Add(-30*24*time.Hour))
		mkVersionFolder(paths.Versions(), "old-2", now.Add(-20*24*time.Hour))
		mkVersionFolder(paths.Versions(), "old-3", now.Add(-10*24*time.Hour))
		mkVersionFolder(paths.Versions(), "recent", now.Add(-time.Hour))

		// "recent" takes the single retention slot as the newest orphan, so
		// the age floor decides the rest: old-3 through old-1 all qualify.
		res, err := supervisor.CleanOrphanVersions(paths, supervisor.VersionGCPolicy{Retain: 1, MinAge: 7 * 24 * time.Hour})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Deleted).To(ConsistOf("old-1", "old-2", "old-3"))
		Expect(res.Kept).To(ConsistOf("recent"))
	})

	It("is a noop when there are no orphans", func() {
		mkVersionFolder(paths.Versions(), "current-v", time.Now())
		Expect(paths.WriteCurrent("current-v")).To(Succeed())

		res, err := supervisor.CleanOrphanVersions(paths, supervisor.VersionGCPolicy{Retain: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Deleted).To(BeEmpty())
	})

	It("handles a missing versions directory", func() {
		nonexistent := supervisor.NewPaths(GinkgoT().TempDir()).Component("never-existed")
		_, err := supervisor.CleanOrphanVersions(nonexistent, supervisor.VersionGCPolicy{Retain: 2})
		Expect(err).NotTo(HaveOccurred())
	})

	It("removes the matching logs when deleting an orphan version", func() {
		mkVersionFolder(paths.Versions(), "current-v", time.Now())
		mkVersionFolder(paths.Versions(), "orphan-v", time.Now())
		Expect(paths.WriteCurrent("current-v")).To(Succeed())

		// Pre-populate app log dirs and flattened process logs, mirroring
		// runtime layout.
		currentLogs := paths.LogsDir("current-v")
		orphanLogs := paths.LogsDir("orphan-v")
		Expect(os.MkdirAll(currentLogs, 0o755)).To(Succeed())
		Expect(os.MkdirAll(orphanLogs, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(currentLogs, "app.log"), []byte("alive"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(orphanLogs, "app.log"), []byte("stale"), 0o644)).To(Succeed())
		currentProcessLog := paths.Log("current-v")
		orphanProcessLog := paths.Log("orphan-v")
		Expect(os.WriteFile(currentProcessLog, []byte("alive"), 0o644)).To(Succeed())
		Expect(os.WriteFile(orphanProcessLog, []byte("stale"), 0o644)).To(Succeed())
		Expect(os.WriteFile(orphanProcessLog+".1", []byte("older"), 0o644)).To(Succeed())

		_, err := supervisor.CleanOrphanVersions(paths, supervisor.VersionGCPolicy{})
		Expect(err).NotTo(HaveOccurred())

		// Orphan version + its logs are gone.
		_, err = os.Stat(filepath.Join(paths.Versions(), "orphan-v"))
		Expect(os.IsNotExist(err)).To(BeTrue())
		_, err = os.Stat(orphanLogs)
		Expect(os.IsNotExist(err)).To(BeTrue())
		_, err = os.Stat(orphanProcessLog)
		Expect(os.IsNotExist(err)).To(BeTrue())
		_, err = os.Stat(orphanProcessLog + ".1")
		Expect(os.IsNotExist(err)).To(BeTrue())

		// Current version + its logs are kept.
		_, err = os.Stat(filepath.Join(paths.Versions(), "current-v"))
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(currentLogs)
		Expect(err).NotTo(HaveOccurred())
		_, err = os.Stat(currentProcessLog)
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("versions.gc state", func() {
	var (
		stateDir string
		cpaths   supervisor.ComponentPaths
	)

	newSupervisor := func() *supervisor.Supervisor {
		cfg := mkTopCfg(stateDir)
		cfg.Supervisor.BindAddress = "127.0.0.1:0"
		// No retention slots, so the 7-day age floor alone decides.
		cfg.VersionFolderRetention = 0
		cfg.Components = []supervisor.ComponentConfig{{
			Name:    "api",
			Port:    freeTCPPort(),
			Command: "/bin/sh ./run.sh",
		}}
		sup, err := supervisor.New(cfg, supervisor.Options{})
		Expect(err).NotTo(HaveOccurred())
		return sup
	}

	BeforeEach(func() {
		stateDir = GinkgoT().TempDir()
		cpaths = supervisor.NewPaths(stateDir).Component("api")
		Expect(cpaths.EnsureDirs()).To(Succeed())
	})

	It("records the startup sweep: last run, count cleaned, and versions size", func() {
		mkVersionFolder(cpaths.Versions(), "ancient", time.Now().Add(-30*24*time.Hour))
		mkVersionFolder(cpaths.Versions(), "current-v", time.Now())
		Expect(cpaths.WriteCurrent("current-v")).To(Succeed())

		snap := newSupervisor().VersionGCStateForTest()

		Expect(snap.Name).To(Equal("versions.gc"))
		Expect(snap.Status).To(Equal(statekit.Pass))
		Expect(snap.Data).To(HaveKeyWithValue("deleted", 1))
		Expect(snap.Data).To(HaveKeyWithValue("deleted_total", int64(1)))
		Expect(snap.Data).To(HaveKeyWithValue("runs", int64(1)))
		Expect(snap.Data["last_run"]).NotTo(BeEmpty())
		Expect(snap.Data).To(HaveKeyWithValue("last_run_ago", "just now"))
		Expect(snap.Data).NotTo(HaveKey("last_error_ago"))
		// Only current-v survives, and it holds one marker file.
		Expect(snap.Data["versions_size_bytes"]).To(BeNumerically(">", 0))
		Expect(snap.Data["components"]).To(HaveKey("api"))
		Expect(snap.Data).NotTo(HaveKey("last_error"))
	})

	It("warns on a failed sweep and keeps the error visible after it recovers", func() {
		skipIfRoot()
		mkVersionFolder(cpaths.Versions(), "ancient", time.Now().Add(-30*24*time.Hour))
		makeReadOnly(cpaths.Versions())

		sup := newSupervisor()
		snap := sup.VersionGCStateForTest()
		Expect(snap.Status).To(Equal(statekit.Warn))
		Expect(snap.Reason).To(ContainSubstring("api:"))
		Expect(snap.Data).To(HaveKeyWithValue("deleted", 0))
		Expect(snap.Data["last_error"]).To(ContainSubstring("ancient"))
		Expect(snap.Data).To(HaveKey("last_error_at"))

		// Once the permission is back the next sweep runs clean. It doesn't
		// finish the job in the same pass: the interrupted delete touched the
		// folder, so its mtime restarted the age window — that is the age
		// floor doing exactly what it promises, and the next sweep gets it.
		Expect(os.Chmod(cpaths.Versions(), 0755)).To(Succeed())
		sup.SweepOrphanVersionsForTest()

		snap = sup.VersionGCStateForTest()
		Expect(snap.Status).To(Equal(statekit.Pass))
		Expect(snap.Data).To(HaveKeyWithValue("deleted", 0))
		// The failure is history now, but still on record.
		Expect(snap.Data["last_error"]).To(ContainSubstring("ancient"))
		Expect(snap.Data).To(HaveKey("last_error_at"))
		Expect(snap.Data).To(HaveKeyWithValue("last_error_ago", "just now"))
	})

	It("keeps the totals across sweeps and reports a clean pass with nothing to do", func() {
		mkVersionFolder(cpaths.Versions(), "ancient", time.Now().Add(-30*24*time.Hour))

		sup := newSupervisor()
		Expect(sup.VersionGCStateForTest().Data).To(HaveKeyWithValue("deleted", 1))

		sup.SweepOrphanVersionsForTest()
		snap := sup.VersionGCStateForTest()
		Expect(snap.Status).To(Equal(statekit.Pass))
		Expect(snap.Data).To(HaveKeyWithValue("deleted", 0))
		Expect(snap.Data).To(HaveKeyWithValue("deleted_total", int64(1)))
		Expect(snap.Data).To(HaveKeyWithValue("runs", int64(2)))
	})
})
