package supervisor

import "os/exec"

// This file holds the platform-independent contract for the cgroup v2 leaf
// hierarchy. The concrete implementation lives in cgroup_linux.go; cgroup_other.go
// returns nil so the whole layer compiles and no-ops on macOS and any non-Linux
// platform. A nil cgroupManager means "no leaves, no enforcement" and every call
// site guards on it, so the supervisor behaves exactly as in phase 1.

// memStat is the memory.stat breakdown for one leaf, the part that explains a
// kill where process RSS looked fine. Bytes; zero when a field is absent.
type memStat struct {
	Anon int64
	File int64
	Slab int64
	Sock int64
}

// memEvents is the memory.events counters for one leaf. They are monotonic
// per leaf lifetime: the supervisor watches them for increments between samples
// to detect throttling (High) and kills (Max/OOMKill).
type memEvents struct {
	High    int64
	Max     int64
	OOMKill int64
}

// cgroupManager owns the cgroup v2 leaf hierarchy: it creates supervisor/,
// workload/, and one leaf per component, charges each child into its leaf,
// writes the derived limits, and reads exact per-leaf usage, stat, events, and
// PSI. It is constructed only on Linux with a writable cgroup v2 mount and the
// memory controller present; newCgroupManager returns nil otherwise.
//
// Every method is best-effort: a read returns ok=false on any failure and a
// write logs and moves on. Nothing here aborts launch, sampling, or shutdown.
type cgroupManager interface {
	// leafAttach returns a hook for LaunchChild that charges the child into the
	// component's leaf. The hook is called with the prepared *exec.Cmd before
	// Start (a no-op today, reserved for clone-into-cgroup); the func it returns
	// is called once after Start with the child pid, writing it into the leaf's
	// cgroup.procs. Returns nil if the component has no leaf.
	leafAttach(component string) func(cmd *exec.Cmd) func(pid int)

	// writeComponentLimits writes the leaf's memory.high (soft), memory.max
	// (hard), and memory.oom.group. A zero high or max is written as "max" (no
	// limit), so an unbudgeted component is bounded only by workload/.
	writeComponentLimits(component string, high, max int64, oomGroup bool)
	// writeWorkloadHigh writes workload/memory.high (the soft pool), the
	// kernel-enforced aggregate backpressure across all components.
	writeWorkloadHigh(high int64)

	readCurrent(component string) (int64, bool)
	readStat(component string) (memStat, bool)
	readEvents(component string) (memEvents, bool)
	readPSISome(component string) (float64, bool)
	readWorkloadCurrent() (int64, bool)
	readContainerPSISome() (float64, bool)
}
