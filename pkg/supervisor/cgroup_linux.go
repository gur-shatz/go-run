//go:build linux

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gur-shatz/go-run/internal/log"
)

// cgroup tree layout under the container's (namespaced) cgroup v2 root. The
// supervisor (PID 1) is moved into supervisor/ so the root can delegate the
// memory controller; every component gets a leaf under workload/ so its charge
// is exact and the kernel can cap it independently.
const (
	cgroupSubtreeControl = cgroupRoot + "/cgroup.subtree_control"
	cgroupProcs          = cgroupRoot + "/cgroup.procs"
	cgroupSupervisorDir  = cgroupRoot + "/supervisor"
	cgroupWorkloadDir    = cgroupRoot + "/workload"
	cgroupRootPressure   = cgroupRoot + "/memory.pressure"
)

// cgroupController is the Linux cgroup v2 implementation of cgroupManager. It is
// created once at supervisor construction (before any child launches) and owns
// the leaf directories for the lifetime of the process.
type cgroupController struct {
	logger *log.Logger
	// leaves maps component name -> absolute leaf directory. Only components
	// present at construction get a leaf; an unknown name yields no-op calls.
	leaves map[string]string
}

// newCgroupManager builds the cgroup v2 leaf hierarchy when the platform can
// support it: a writable cgroup v2 mount with the memory controller present. It
// returns nil (tracking-only, no enforcement) on any failure, so a detection or
// permission problem degrades the mode rather than aborting startup.
func newCgroupManager(mode MemoryMode, components []string, logger *log.Logger) cgroupManager {
	if mode != MemoryModeCgroup2 {
		return nil
	}
	if !cgroupMountWritable() {
		logger.Status("memory: cgroup2 mount not writable; enforcement disabled, tracking via RSS")
		return nil
	}

	this := &cgroupController{logger: logger, leaves: make(map[string]string, len(components))}
	if err := this.setupTree(components); err != nil {
		logger.Warn("memory: cgroup tree setup failed (%v); enforcement disabled, tracking via RSS", err)
		return nil
	}
	logger.Status("memory: cgroup2 leaf hierarchy ready (%d leaves under %s)", len(this.leaves), cgroupWorkloadDir)
	return this
}

// cgroupMountWritable reports whether the cgroup root is a writable mount we can
// arrange. The cheap probe is whether cgroup.subtree_control is writable.
func cgroupMountWritable() bool {
	f, err := os.OpenFile(cgroupSubtreeControl, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// setupTree arranges the container cgroup into supervisor/ + workload/<leaf>.
// It honors the cgroup v2 "no internal processes" rule by moving the
// supervisor's processes out of the root before delegating controllers.
func (this *cgroupController) setupTree(components []string) error {
	// 1. Create supervisor/ and move every process in the root (PID 1 and any
	//    threads) into it, so the root holds no processes and may delegate.
	if err := os.MkdirAll(cgroupSupervisorDir, 0o755); err != nil {
		return err
	}
	if err := moveProcsToLeaf(cgroupProcs, filepath.Join(cgroupSupervisorDir, "cgroup.procs")); err != nil {
		return err
	}

	// 2. Create workload/ and delegate the memory controller down both levels.
	if err := os.MkdirAll(cgroupWorkloadDir, 0o755); err != nil {
		return err
	}
	if err := enableMemoryController(cgroupSubtreeControl); err != nil {
		return err
	}
	if err := enableMemoryController(filepath.Join(cgroupWorkloadDir, "cgroup.subtree_control")); err != nil {
		return err
	}

	// 3. One leaf per component. A leaf created after workload/ delegates +memory
	//    inherits the memory.* control files.
	for _, name := range components {
		leaf := filepath.Join(cgroupWorkloadDir, sanitizeLeafName(name))
		if err := os.MkdirAll(leaf, 0o755); err != nil {
			return err
		}
		this.leaves[name] = leaf
	}
	return nil
}

// moveProcsToLeaf reads pids from a cgroup.procs file and writes each into dst.
// cgroup.procs accepts one pid per write; a pid that has already exited yields a
// best-effort skip.
func moveProcsToLeaf(srcProcs, dstProcs string) error {
	data, err := os.ReadFile(srcProcs)
	if err != nil {
		return err
	}
	for _, line := range strings.Fields(string(data)) {
		if err := os.WriteFile(dstProcs, []byte(line), 0); err != nil {
			// A pid may have exited between read and write; skip it rather than
			// failing the whole move.
			continue
		}
	}
	return nil
}

// enableMemoryController writes "+memory" into a subtree_control file. A leading
// read of the current value avoids re-enabling (harmless but noisy in logs).
func enableMemoryController(subtreeControl string) error {
	if data, err := os.ReadFile(subtreeControl); err == nil {
		for _, ctrl := range strings.Fields(string(data)) {
			if ctrl == "memory" {
				return nil
			}
		}
	}
	return os.WriteFile(subtreeControl, []byte("+memory"), 0)
}

// sanitizeLeafName keeps a component name usable as a single cgroup directory
// component: slashes (the only structurally dangerous character) become '_'.
// Component names are already filesystem-safe (they are state_dir subdirs), so
// this is belt-and-braces.
func sanitizeLeafName(name string) string {
	return strings.ReplaceAll(name, "/", "_")
}

func (this *cgroupController) leafAttach(component string) func(cmd *exec.Cmd) func(pid int) {
	leaf, ok := this.leaves[component]
	if !ok {
		return nil
	}
	procs := filepath.Join(leaf, "cgroup.procs")
	return func(_ *exec.Cmd) func(pid int) {
		// Charge after Start by writing the pid into the leaf. The child and any
		// workers it forks are charged here from that point; the brief window
		// where it is charged to supervisor/ is acceptable (see design). Using a
		// post-Start write keeps launch non-breaking on every kernel — a failed
		// clone-into-cgroup can never abort Start this way.
		return func(pid int) {
			if pid <= 0 {
				return
			}
			if err := os.WriteFile(procs, []byte(strconv.Itoa(pid)), 0); err != nil {
				this.logger.Warn("memory: charge %s pid %d into leaf failed: %v", component, pid, err)
			}
		}
	}
}

func (this *cgroupController) writeComponentLimits(component string, high, max int64, oomGroup bool) {
	leaf, ok := this.leaves[component]
	if !ok {
		return
	}
	this.writeLimitFile(filepath.Join(leaf, "memory.high"), high)
	this.writeLimitFile(filepath.Join(leaf, "memory.max"), max)
	oom := "0"
	if oomGroup {
		oom = "1"
	}
	if err := os.WriteFile(filepath.Join(leaf, "memory.oom.group"), []byte(oom), 0); err != nil {
		this.logger.Warn("memory: set %s oom.group failed: %v", component, err)
	}
}

func (this *cgroupController) writeWorkloadHigh(high int64) {
	this.writeLimitFile(filepath.Join(cgroupWorkloadDir, "memory.high"), high)
}

// writeLimitFile writes a byte limit, or the literal "max" (no limit) when v<=0,
// matching cgroup v2 semantics for memory.high / memory.max.
func (this *cgroupController) writeLimitFile(path string, v int64) {
	val := "max"
	if v > 0 {
		val = strconv.FormatInt(v, 10)
	}
	if err := os.WriteFile(path, []byte(val), 0); err != nil {
		this.logger.Warn("memory: write %s=%s failed: %v", path, val, err)
	}
}

func (this *cgroupController) readCurrent(component string) (int64, bool) {
	leaf, ok := this.leaves[component]
	if !ok {
		return 0, false
	}
	return readCgroupUintFile(filepath.Join(leaf, "memory.current"))
}

func (this *cgroupController) readStat(component string) (memStat, bool) {
	leaf, ok := this.leaves[component]
	if !ok {
		return memStat{}, false
	}
	return readMemoryStat(filepath.Join(leaf, "memory.stat"))
}

func (this *cgroupController) readEvents(component string) (memEvents, bool) {
	leaf, ok := this.leaves[component]
	if !ok {
		return memEvents{}, false
	}
	return readMemoryEvents(filepath.Join(leaf, "memory.events"))
}

func (this *cgroupController) readPSISome(component string) (float64, bool) {
	leaf, ok := this.leaves[component]
	if !ok {
		return 0, false
	}
	return readPSISomeAvg10(filepath.Join(leaf, "memory.pressure"))
}

func (this *cgroupController) readWorkloadCurrent() (int64, bool) {
	return readCgroupUintFile(filepath.Join(cgroupWorkloadDir, "memory.current"))
}

func (this *cgroupController) readContainerPSISome() (float64, bool) {
	return readPSISomeAvg10(cgroupRootPressure)
}

// parseCgroupKV reads a cgroup "key value" file (memory.stat, memory.events)
// and calls assign for each well-formed line. It returns false only when the
// file cannot be read; malformed lines are skipped.
func parseCgroupKV(path string, assign func(key string, v int64)) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		assign(fields[0], v)
	}
	return true
}

// readMemoryStat parses the key/value memory.stat file into the anon/file/slab/
// sock split plus inactive_file. Missing keys stay zero.
func readMemoryStat(path string) (memStat, bool) {
	var st memStat
	ok := parseCgroupKV(path, func(key string, v int64) {
		switch key {
		case "anon":
			st.Anon = v
		case "file":
			st.File = v
		case "slab":
			st.Slab = v
		case "sock":
			st.Sock = v
		case "inactive_file", "total_inactive_file":
			st.InactiveFile = v
		}
	})
	return st, ok
}

// readMemoryEvents parses the memory.events counters (high/max/oom_kill).
func readMemoryEvents(path string) (memEvents, bool) {
	var ev memEvents
	ok := parseCgroupKV(path, func(key string, v int64) {
		switch key {
		case "high":
			ev.High = v
		case "max":
			ev.Max = v
		case "oom_kill":
			ev.OOMKill = v
		}
	})
	return ev, ok
}

// readPSISomeAvg10 parses the "some avg10=<pct> ..." line of a memory.pressure
// file and returns the avg10 stall percentage as a 0..1 ratio.
func readPSISomeAvg10(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "some ")
		if !ok {
			continue
		}
		for _, field := range strings.Fields(rest) {
			val, ok := strings.CutPrefix(field, "avg10=")
			if !ok {
				continue
			}
			pct, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return 0, false
			}
			return pct / 100, true
		}
	}
	return 0, false
}
