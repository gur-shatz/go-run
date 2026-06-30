//go:build !linux

package supervisor

import (
	"os/exec"
	"strconv"
	"strings"
)

// On non-Linux platforms (macOS dev) there is no cgroup filesystem. Mode
// detection reports no cgroup tier, so resolveMemoryMode falls back to host
// tracking; the global limit is only known if injected via the env var.

func detectCgroupMode() MemoryMode { return "" }

func readCgroupGlobalLimit() (int64, string, bool) { return 0, "", false }

func readContainerCurrentBytes() (int64, bool) { return 0, false }

// readMachineTotalBytes returns the host's total physical RAM. On macOS this is
// sysctl hw.memsize (already in bytes). Shown for context only.
func readMachineTotalBytes() (int64, bool) {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}
