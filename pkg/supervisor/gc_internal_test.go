package supervisor

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/statekit"
)

var _ = Describe("versionGCLiveState", func() {
	It("computes the ago fields when the state is read, not when the sweep wrote it", func() {
		leaf := statekit.NewManualState("versions.gc")
		tracker := &versionGCTracker{}
		live := &versionGCLiveState{underlying: leaf, tracker: tracker}

		// A sweep two hours ago that also recorded an error.
		twoHoursAgo := time.Now().Add(-2 * time.Hour)
		rep := tracker.record(twoHoursAgo, 3, map[string]int64{"api": 1024}, "api: boom")
		leaf.Warn(rep.LastError, map[string]any{"deleted": rep.Deleted})

		snap := live.Snapshot()
		Expect(snap.Data).To(HaveKeyWithValue("deleted", 3))
		Expect(snap.Data).To(HaveKeyWithValue("last_run_ago", "2h 0m ago"))
		Expect(snap.Data).To(HaveKeyWithValue("last_error_ago", "2h 0m ago"))
	})

	It("adds nothing before the first sweep", func() {
		leaf := statekit.NewManualState("versions.gc")
		leaf.Pass("no sweep yet", nil)
		live := &versionGCLiveState{underlying: leaf, tracker: &versionGCTracker{}}

		Expect(live.Snapshot().Data).NotTo(HaveKey("last_run_ago"))
	})
})
