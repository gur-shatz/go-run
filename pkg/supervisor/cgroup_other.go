//go:build !linux

package supervisor

import "github.com/gur-shatz/go-run/internal/log"

// On non-Linux platforms there is no cgroup filesystem, so the leaf hierarchy
// and enforcement are unavailable. newCgroupManager always returns a nil
// manager: the supervisor tracks via host process RSS and never enforces, which
// is exactly the phase-1 behavior on the macOS dev box.
func newCgroupManager(_ MemoryMode, _ []string, _ *log.Logger) cgroupManager { return nil }
