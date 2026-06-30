//go:build linux

package supervisor

import (
	"os"
	"strconv"
	"strings"
)

// readProcessRSS returns the resident set size of a process in bytes, read
// cheaply from /proc/<pid>/status (VmRSS, reported in kB). ok is false if the
// process is gone or the field is missing. This is the universally-available
// per-component figure in the tracking-only iteration; it does not require the
// cgroup leaves that enforcement will later add.
func readProcessRSS(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "VmRSS:")
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
