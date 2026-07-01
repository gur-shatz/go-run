package supervisor

import (
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/log"
)

var _ = Describe("Memory enforcement", func() {
	Describe("pod-pressure shedding picks the largest killable component", func() {
		It("returns not-ok on an empty set", func() {
			_, ok := pickLargest(nil)
			Expect(ok).To(BeFalse())
		})

		It("picks the component using the most memory", func() {
			best, ok := pickLargest([]killTarget{
				{name: "gateway", cur: 300},
				{name: "backend", cur: 800},
				{name: "frontend", cur: 100},
			})
			Expect(ok).To(BeTrue())
			Expect(best.name).To(Equal("backend"))
		})
	})

	Describe("supervisor 'choose' enforcement is decoupled from cgroups (testable on host)", func() {
		buildMonitor := func(cfg Config) *memoryMonitor {
			cfg.ApplyDefaults()
			return newMemoryMonitor(cfg, NewPaths(GinkgoT().TempDir()), newStatekitBundle(cfg), nil, log.New("[t]", false))
		}
		cfgWithLimit := func(enforce *bool) Config {
			return Config{
				Memory: &MemoryConfig{LimitBytes: 512 << 20, Enforce: enforce},
				Components: []ComponentConfig{
					{Name: "gateway", Port: 8080, Command: "/bin/g",
						Memory: &ComponentMemoryConfig{Share: 0.4}},
				},
			}
		}

		It("creates the enforcer with a resolved limit and enforce on, even with no cgroup", func() {
			// On the macOS dev box there is no cgroup at all — this proves the
			// supervisor 'choose' actions run in host mode.
			if runtime.GOOS != "darwin" {
				Skip("host-mode (no-cgroup) assertion is exercised on the darwin dev box")
			}
			mon := buildMonitor(cfgWithLimit(nil)) // enforce defaults on
			Expect(mon).NotTo(BeNil())
			Expect(mon.cgroup).To(BeNil(), "no cgroup on host")
			Expect(mon.enforce).NotTo(BeNil(), "enforcement must run without cgroups")
		})

		It("does not create the enforcer when enforce is off (any platform)", func() {
			off := false
			mon := buildMonitor(cfgWithLimit(&off))
			Expect(mon.enforce).To(BeNil())
		})
	})

	Describe("fail-open: degraded subsystem drives the memory.subsystem state", func() {
		enabled := true
		newBundleWithGateway := func() *statekitBundle {
			return newStatekitBundle(Config{
				Memory: &MemoryConfig{Enabled: &enabled},
				Components: []ComponentConfig{
					{Name: "gateway", Port: 8080, Command: "/bin/g",
						Memory: &ComponentMemoryConfig{Share: 0.4}},
				},
			})
		}

		It("registers a supervisor-scoped memory.subsystem statekit state", func() {
			b := newBundleWithGateway()
			Expect(b.memorySubsystem).NotTo(BeNil())
			Expect(b.memorySubsystem.Name()).To(Equal("memory.subsystem"))
		})

		It("marks the subsystem state pass when healthy", func() {
			b := newBundleWithGateway()
			b.observeMemorySubsystemHealthy("cgroup2: tracking + enforcing")
			Expect(b.memorySubsystem.Snapshot().Status.String()).To(Equal("pass"))
		})

		It("raises the subsystem state to warn when degraded, without touching component leaves", func() {
			b := newBundleWithGateway()
			b.observeMemory("gateway", 100, 200, 300, memStateOK) // component itself is fine
			b.observeMemorySubsystemDegraded("cgroup2 detected but leaf hierarchy unavailable")

			sub := b.memorySubsystem.Snapshot()
			Expect(sub.Status.String()).To(Equal("warn"))
			Expect(sub.Reason).To(ContainSubstring("leaf hierarchy unavailable"))
			// The component's own memory leaf stays pass: subsystem degradation is
			// not a per-component memory problem.
			Expect(b.components["gateway"].memory.Snapshot().Status.String()).To(Equal("pass"))
		})

		It("leaves the subsystem state unregistered when memory is disabled", func() {
			disabled := false
			b := newStatekitBundle(Config{Memory: &MemoryConfig{Enabled: &disabled}})
			Expect(b.memorySubsystem).To(BeNil())
			// The observe methods are safe no-ops in that case.
			Expect(func() { b.observeMemorySubsystemDegraded("x") }).NotTo(Panic())
		})

		It("markDegraded is a safe no-op on a nil monitor", func() {
			var mon *memoryMonitor
			Expect(func() { mon.markDegraded("x") }).NotTo(Panic())
			Expect(mon.degradedReason()).To(BeEmpty())
		})
	})
})
