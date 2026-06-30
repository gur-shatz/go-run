//go:build linux

package supervisor

import (
	"os"
	"strconv"
	"strings"
)

// cgroup filesystem locations. Inside a container the mount is cgroup-namespaced
// so /sys/fs/cgroup is the container's own root.
const (
	cgroupRoot           = "/sys/fs/cgroup"
	cgroup2Controllers   = cgroupRoot + "/cgroup.controllers"
	cgroup2MemoryMax     = cgroupRoot + "/memory.max"
	cgroup2MemoryCurrent = cgroupRoot + "/memory.current"
	cgroup1MemoryDir     = cgroupRoot + "/memory"
	cgroup1MemoryLimit   = cgroup1MemoryDir + "/memory.limit_in_bytes"
	cgroup1MemoryUsage   = cgroup1MemoryDir + "/memory.usage_in_bytes"
)

// cgroup1UnlimitedThreshold filters the v1 "unlimited" sentinel (a value close
// to the max int64, page-aligned). Any real container limit is far below this.
const cgroup1UnlimitedThreshold = int64(1) << 62

// detectCgroupMode reports the cgroup capability tier, or "" if no cgroup
// memory hierarchy is usable. cgroup v2 is identified by the unified
// cgroup.controllers file listing "memory"; v1 by the memory subsystem dir.
func detectCgroupMode() MemoryMode {
	if data, err := os.ReadFile(cgroup2Controllers); err == nil {
		for _, ctrl := range strings.Fields(string(data)) {
			if ctrl == "memory" {
				return MemoryModeCgroup2
			}
		}
	}
	if fi, err := os.Stat(cgroup1MemoryDir); err == nil && fi.IsDir() {
		return MemoryModeCgroup1
	}
	return ""
}

// readCgroupGlobalLimit returns the container-level memory limit the kernel
// knows about, with the source that won. The env var is tried first by the
// caller; this is the fallback. "max" (v2) or the v1 unlimited sentinel mean no
// container-level limit and yield ok=false.
func readCgroupGlobalLimit() (int64, string, bool) {
	switch detectCgroupMode() {
	case MemoryModeCgroup2:
		if v, ok := readCgroupLimitFile(cgroup2MemoryMax); ok {
			return v, "cgroup2:memory.max", true
		}
	case MemoryModeCgroup1:
		if v, ok := readCgroupLimitFile(cgroup1MemoryLimit); ok {
			return v, "cgroup1:memory.limit_in_bytes", true
		}
	}
	return 0, "", false
}

// readContainerCurrentBytes returns the container cgroup's current memory
// charge (includes page cache and slab, so it exceeds summed process RSS).
func readContainerCurrentBytes() (int64, bool) {
	switch detectCgroupMode() {
	case MemoryModeCgroup2:
		return readCgroupUintFile(cgroup2MemoryCurrent)
	case MemoryModeCgroup1:
		return readCgroupUintFile(cgroup1MemoryUsage)
	}
	return 0, false
}

// readMachineTotalBytes returns the host's total physical RAM from
// /proc/meminfo (MemTotal, reported in kB). This is the whole machine, NOT the
// pod's budget — it is shown for context only and never used to derive limits.
func readMachineTotalBytes() (int64, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "MemTotal:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest) // "<kb> kB"
		if len(fields) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// readCgroupLimitFile parses a limit file, skipping the v2 literal "max" and the
// v1 unlimited sentinel.
func readCgroupLimitFile(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(raw))
	if s == "max" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 || v >= cgroup1UnlimitedThreshold {
		return 0, false
	}
	return v, true
}

// readCgroupUintFile parses a plain unsigned-integer cgroup file.
func readCgroupUintFile(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}
