package supervisor

import (
	"os"
	"strconv"
	"strings"
)

// MemoryMode is the resolved operating mode of the memory subsystem. It records
// which capability tier is active so it is always clear what the figures mean
// and why enforcement is or is not available. This iteration tracks and
// displays in every mode but never enforces.
type MemoryMode string

const (
	// MemoryModeCgroup2 is Linux with a cgroup v2 hierarchy and the memory
	// controller present. The pod-level limit and container usage come from
	// the cgroup; per-component usage is sampled from process RSS.
	MemoryModeCgroup2 MemoryMode = "cgroup2"
	// MemoryModeCgroup1 is Linux with only a cgroup v1 memory hierarchy.
	MemoryModeCgroup1 MemoryMode = "cgroup1"
	// MemoryModeHost is tracking via host process APIs with no cgroup (macOS
	// dev, or a Linux node without a usable cgroup mount). Per-component RSS is
	// still available; the pod limit is only known if injected via the env var.
	MemoryModeHost MemoryMode = "host"
	// MemoryModeDisabled is no sampling, no display, no enforcement. Resolved
	// whenever the subsystem is off or the platform offers nothing to sample.
	MemoryModeDisabled MemoryMode = "disabled"
)

// resolveMemoryMode picks the operating mode from config and platform
// capability. A disabled or absent block is always MemoryModeDisabled; any
// detection failure degrades to a weaker mode rather than erroring.
func resolveMemoryMode(m *MemoryConfig) MemoryMode {
	if !m.IsEnabled() {
		return MemoryModeDisabled
	}
	if mode := detectCgroupMode(); mode != "" {
		return mode
	}
	// No cgroup hierarchy. Process RSS is still sampleable via host APIs on
	// every platform we run on (Linux /proc, macOS ps), so report host.
	return MemoryModeHost
}

// globalLimit is the resolved pod memory limit and the source that won.
type globalLimit struct {
	Bytes  int64  // 0 means unresolved (tracking-only, no budgets derivable)
	Source string // e.g. "env:MEMORY_LIMIT_BYTES", "cgroup2:memory.max", "unresolved"
}

// Resolved reports whether a real limit was found.
func (this globalLimit) Resolved() bool { return this.Bytes > 0 }

// resolveGlobalLimit tries the env var first (preferred, always matches the pod
// spec when set via the Downward API), then the kernel's own view of the
// container limit. The first real value wins. An unresolved limit is not fatal:
// the supervisor tracks and displays usage without derivable budgets.
func resolveGlobalLimit(m *MemoryConfig) globalLimit {
	envVar := "MEMORY_LIMIT_BYTES"
	if m != nil && m.LimitEnvVar != "" {
		envVar = m.LimitEnvVar
	}
	if raw := strings.TrimSpace(os.Getenv(envVar)); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			return globalLimit{Bytes: v, Source: "env:" + envVar}
		}
	}
	// Explicit config override (dev/local fallback), after the env var so a
	// real pod's Downward API value always wins.
	if m != nil && m.LimitBytes > 0 {
		return globalLimit{Bytes: m.LimitBytes, Source: "config:limit_bytes"}
	}
	if v, source, ok := readCgroupGlobalLimit(); ok && v > 0 {
		return globalLimit{Bytes: v, Source: source}
	}
	return globalLimit{Bytes: 0, Source: "unresolved"}
}

// componentBudget is a component's derived slice of the workload budget. Share
// is always meaningful; HighBytes/LimitBytes are 0 when the global limit is
// unresolved or the component has no share (tracked but unbudgeted).
type componentBudget struct {
	Share      float64
	HighBytes  int64 // derived soft limit (would be memory.high under enforcement)
	LimitBytes int64 // derived hard limit (would be memory.max under enforcement)
}

// memoryPools is the two-tier workload budget derived from the global limit.
type memoryPools struct {
	HardPool int64 // L*(1-reserve)
	SoftPool int64 // L*(1-reserve-headroom)
}

// derivePools computes the soft and hard workload pools from the global limit
// and the two reserves. Returns a zero-value memoryPools when L is unresolved.
func derivePools(L int64, m *MemoryConfig) memoryPools {
	if L <= 0 || m == nil {
		return memoryPools{}
	}
	hard := float64(L) * (1 - m.SupervisorReserve)
	soft := float64(L) * (1 - m.SupervisorReserve - m.CacheHeadroom)
	if soft < 0 {
		soft = 0
	}
	return memoryPools{HardPool: int64(hard), SoftPool: int64(soft)}
}

// deriveBudgets partitions the workload pools across components by share. With
// an unresolved limit it still records each share so the policy is visible,
// just with zero byte budgets.
func deriveBudgets(L int64, m *MemoryConfig, comps []ComponentConfig) map[string]componentBudget {
	pools := derivePools(L, m)
	// Soft-below-hard ratio for an absolute hardlimit: the same page-cache
	// headroom the pool math reserves, so the warn band sits just under the hard
	// cap (default 10% below). Falls back to no band if headroom is >= 1.
	softRatio := 1 - m.CacheHeadroom
	if softRatio < 0 {
		softRatio = 0
	}

	out := make(map[string]componentBudget, len(comps))
	for _, c := range comps {
		if c.Memory == nil {
			continue
		}
		b := componentBudget{Share: c.Memory.Share}
		switch {
		case c.Memory.HardLimit > 0 || c.Memory.SoftLimit > 0:
			// Absolute budget: independent of the resolved global limit.
			b.LimitBytes = int64(c.Memory.HardLimit)
			switch {
			case c.Memory.SoftLimit > 0:
				b.HighBytes = int64(c.Memory.SoftLimit) // explicit warn band
			case b.LimitBytes > 0:
				b.HighBytes = int64(float64(b.LimitBytes) * softRatio) // derived just below hard
			}
		case pools.SoftPool > 0 && c.Memory.Share > 0:
			// Relative budget: a slice of the workload pools (needs a resolved L).
			b.HighBytes = int64(float64(pools.SoftPool) * c.Memory.Share)
			b.LimitBytes = int64(float64(pools.HardPool) * c.Memory.Share)
		}
		out[c.Name] = b
	}
	return out
}

// anyBudgeted reports whether at least one component has a real hard budget
// (from a share + resolved limit, or an absolute hardlimit). Without one there
// are no warn/fail states for the enforcer to react to.
func anyBudgeted(budgets map[string]componentBudget) bool {
	for _, b := range budgets {
		if b.LimitBytes > 0 {
			return true
		}
	}
	return false
}

// Memory assessment states, mirroring the (future) enforcement vocabulary.
const (
	memStateOK   = "ok"
	memStateSoft = "soft"
	memStateHard = "hard"
)

// classifyMemoryState grades current usage against the derived budget. Empty
// string means "no budget known" (tracking-only), so callers can render a
// neutral figure rather than a misleading "ok".
func classifyMemoryState(current, high, limit int64) string {
	switch {
	case limit > 0 && current >= limit:
		return memStateHard
	case high > 0 && current >= high:
		return memStateSoft
	case high > 0:
		return memStateOK
	default:
		return ""
	}
}
