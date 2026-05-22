package supervisor

import "time"

// Counters tracks bad-version evidence for one current-version install. The
// supervisor uses two independent time dials:
//
//   - CrashWindow (default 1m): an exit with uptime below this counts as a
//     fast crash, incrementing FastCrashes. Longer-lived exits don't.
//   - StabilityTime (handled outside this struct, default 5m): when the
//     child achieves this much continuous uptime, Reset() wipes both
//     counters (recovery), so a transient flap doesn't accumulate forever.
//
// Recovery is automatic. Even a rejected version that the supervisor keeps
// retrying (no-stable case) will see counters reset once it actually runs
// past StabilityTime, taking the supervisor out of panic mode without
// operator intervention.
type Counters struct {
	FastCrashes  int
	ExecFailures int

	CrashWindow       time.Duration
	CrashThreshold    int
	ExecFailThreshold int
}

// NewCounters returns a fresh Counters with the given thresholds.
func NewCounters(crashWindow time.Duration, crashThreshold, execFailThreshold int) *Counters {
	return &Counters{
		CrashWindow:       crashWindow,
		CrashThreshold:    crashThreshold,
		ExecFailThreshold: execFailThreshold,
	}
}

// OnExit records a child exit. Exits with uptime below CrashWindow count as
// fast crashes; longer-lived exits are normal restarts under backoff and do
// not feed the counter.
func (this *Counters) OnExit(launchedAt, exitedAt time.Time) {
	if exitedAt.Sub(launchedAt) < this.CrashWindow {
		this.FastCrashes++
	}
}

// OnExecFailure records that the supervisor could not start the child at all
// (ENOENT, EACCES, malformed binary, missing template variable, etc.).
func (this *Counters) OnExecFailure() {
	this.ExecFailures++
}

// ShouldReject reports whether either counter has tripped its threshold,
// meaning the current version should be appended to rejects.txt.
func (this *Counters) ShouldReject() bool {
	return this.FastCrashes >= this.CrashThreshold || this.ExecFailures >= this.ExecFailThreshold
}

// Reset zeroes the counters. Called when the child reaches StabilityTime and
// on demote when swapping to a different stable.
func (this *Counters) Reset() {
	this.FastCrashes = 0
	this.ExecFailures = 0
}
