//go:build !linux

package supervisor

import (
	"os/exec"
	"strconv"
	"strings"
)

// readProcessRSS returns the resident set size of a process in bytes. With no
// /proc on macOS, it shells out to ps, whose rss column is in kB. ok is false
// if the process is gone or ps is unavailable. This is the host-mode sampler
// the dashboard uses on a dev machine; it is intentionally simple.
func readProcessRSS(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, false
	}
	kb, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return kb * 1024, true
}

// readProcessPSS has no cheap equivalent on macOS (no smaps_rollup), so PSS is
// simply unavailable in host mode. The caller omits the field.
func readProcessPSS(_ int) (int64, bool) { return 0, false }
