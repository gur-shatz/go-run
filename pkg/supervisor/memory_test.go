package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Memory subsystem", func() {
	Describe("budget derivation", func() {
		// L = 512Mi, reserves 0.08 / 0.10 -> hard ~471Mi, soft ~420Mi.
		L := int64(512 << 20)
		mcfg := &MemoryConfig{SupervisorReserve: 0.08, CacheHeadroom: 0.10}

		It("derives soft and hard pools from the global limit", func() {
			pools := derivePools(L, mcfg)
			Expect(pools.HardPool).To(BeNumerically("~", int64(float64(L)*0.92), 2))
			Expect(pools.SoftPool).To(BeNumerically("~", int64(float64(L)*0.82), 2))
			Expect(pools.SoftPool).To(BeNumerically("<", pools.HardPool))
		})

		It("partitions pools across component shares", func() {
			comps := []ComponentConfig{
				{Name: "gateway", Memory: &ComponentMemoryConfig{Share: 0.40}},
				{Name: "backend", Memory: &ComponentMemoryConfig{Share: 0.25}},
				{Name: "frontend"}, // no memory block -> not in budgets
			}
			b := deriveBudgets(L, mcfg, comps)

			Expect(b).To(HaveKey("gateway"))
			Expect(b).To(HaveKey("backend"))
			Expect(b).NotTo(HaveKey("frontend"))

			// gateway ~168Mi soft, ~188Mi hard.
			Expect(b["gateway"].HighBytes).To(BeNumerically("~", 168<<20, 1<<20))
			Expect(b["gateway"].LimitBytes).To(BeNumerically("~", 188<<20, 1<<20))
			// Each component's soft budget is below its hard budget.
			Expect(b["gateway"].HighBytes).To(BeNumerically("<", b["gateway"].LimitBytes))
			// Sum of hard budgets stays under the hard pool (reserve preserved).
			pools := derivePools(L, mcfg)
			Expect(b["gateway"].LimitBytes + b["backend"].LimitBytes).To(BeNumerically("<=", pools.HardPool))
		})

		It("records shares but zero byte budgets when the limit is unresolved", func() {
			comps := []ComponentConfig{{Name: "gateway", Memory: &ComponentMemoryConfig{Share: 0.40}}}
			b := deriveBudgets(0, mcfg, comps)
			Expect(b["gateway"].Share).To(Equal(0.40))
			Expect(b["gateway"].HighBytes).To(BeZero())
			Expect(b["gateway"].LimitBytes).To(BeZero())
		})

		It("doubles every budget when the pod is resized, with no config change", func() {
			comps := []ComponentConfig{{Name: "gateway", Memory: &ComponentMemoryConfig{Share: 0.40}}}
			small := deriveBudgets(L, mcfg, comps)["gateway"]
			large := deriveBudgets(2*L, mcfg, comps)["gateway"]
			Expect(large.LimitBytes).To(BeNumerically("~", 2*small.LimitBytes, 2))
		})
	})

	Describe("state classification", func() {
		It("grades current usage against the derived budget", func() {
			Expect(classifyMemoryState(100, 200, 300)).To(Equal(memStateOK))
			Expect(classifyMemoryState(200, 200, 300)).To(Equal(memStateSoft))
			Expect(classifyMemoryState(250, 200, 300)).To(Equal(memStateSoft))
			Expect(classifyMemoryState(300, 200, 300)).To(Equal(memStateHard))
		})
		It("returns empty when no budget is known (tracking-only)", func() {
			Expect(classifyMemoryState(100, 0, 0)).To(Equal(""))
		})
	})

	Describe("resolveGlobalLimit", func() {
		It("prefers the configured env var", func() {
			const envName = "TEST_MEM_LIMIT_BYTES"
			os.Setenv(envName, "536870912")
			defer os.Unsetenv(envName)

			lim := resolveGlobalLimit(&MemoryConfig{LimitEnvVar: envName})
			Expect(lim.Resolved()).To(BeTrue())
			Expect(lim.Bytes).To(Equal(int64(536870912)))
			Expect(lim.Source).To(Equal("env:" + envName))
		})
		It("is unresolved when nothing yields a real value", func() {
			lim := resolveGlobalLimit(&MemoryConfig{LimitEnvVar: "DEFINITELY_UNSET_MEM_LIMIT"})
			// On a dev box with no cgroup this is unresolved; on a cgroup host it
			// may resolve from the kernel. Only assert the env path is not taken.
			if !lim.Resolved() {
				Expect(lim.Source).To(Equal("unresolved"))
			}
		})
		It("falls back to the config limit_bytes when no env var is set", func() {
			os.Unsetenv("TEST_CFG_LIMIT")
			lim := resolveGlobalLimit(&MemoryConfig{LimitEnvVar: "TEST_CFG_LIMIT", LimitBytes: 268435456})
			Expect(lim.Bytes).To(Equal(int64(268435456)))
			Expect(lim.Source).To(Equal("config:limit_bytes"))
		})
		It("prefers the env var over the config limit_bytes", func() {
			const envName = "TEST_CFG_LIMIT_PREF"
			os.Setenv(envName, "536870912")
			defer os.Unsetenv(envName)
			lim := resolveGlobalLimit(&MemoryConfig{LimitEnvVar: envName, LimitBytes: 1})
			Expect(lim.Bytes).To(Equal(int64(536870912)))
			Expect(lim.Source).To(Equal("env:" + envName))
		})
		It("ignores a non-numeric env value", func() {
			const envName = "TEST_MEM_LIMIT_BAD"
			os.Setenv(envName, "not-a-number")
			defer os.Unsetenv(envName)
			lim := resolveGlobalLimit(&MemoryConfig{LimitEnvVar: envName})
			Expect(lim.Source).NotTo(Equal("env:" + envName))
		})
	})

	Describe("resolveMemoryMode", func() {
		off := false
		It("is disabled only when explicitly turned off", func() {
			Expect(resolveMemoryMode(&MemoryConfig{Enabled: &off})).To(Equal(MemoryModeDisabled))
		})
		It("measures by default with no block or an unset flag", func() {
			// On this platform it is host (no cgroup) or a cgroup tier on Linux;
			// either way it is never disabled when on.
			Expect(resolveMemoryMode(nil)).NotTo(Equal(MemoryModeDisabled))
			Expect(resolveMemoryMode(&MemoryConfig{})).NotTo(Equal(MemoryModeDisabled))
		})
	})

	Describe("enrichSnapshot", func() {
		It("fills memory fields from the latest sample", func() {
			mon := &memoryMonitor{ringMax: 8}
			mon.store(memorySample{
				Components: []componentMemory{{
					Name: "gateway", CurrentBytes: 150, HighBytes: 160, LimitBytes: 200, State: memStateOK,
				}},
			})
			snap := ComponentSnapshot{Name: "gateway"}
			mon.enrichSnapshot(&snap)
			Expect(snap.MemoryCurrentBytes).To(Equal(int64(150)))
			Expect(snap.MemoryLimitBytes).To(Equal(int64(200)))
			Expect(snap.MemoryState).To(Equal(memStateOK))
			Expect(snap.MemoryPressureRatio).To(BeNumerically("~", 0.75, 0.001))
		})
		It("is a safe no-op on a nil monitor and unknown component", func() {
			var mon *memoryMonitor
			snap := ComponentSnapshot{Name: "x"}
			Expect(func() { mon.enrichSnapshot(&snap) }).NotTo(Panic())
			Expect(snap.MemoryCurrentBytes).To(BeZero())
		})
	})

	Describe("componentSeries ring", func() {
		It("returns the per-component points in order", func() {
			mon := &memoryMonitor{ringMax: 4}
			for i := int64(1); i <= 3; i++ {
				mon.store(memorySample{
					TS:         time.Unix(i, 0).UTC().Format(time.RFC3339),
					Components: []componentMemory{{Name: "g", CurrentBytes: i * 100}},
				})
			}
			pts := mon.componentSeries("g", 0)
			Expect(pts).To(HaveLen(3))
			Expect(pts[0].CurrentBytes).To(Equal(int64(100)))
			Expect(pts[2].CurrentBytes).To(Equal(int64(300)))
		})
		It("drops the oldest beyond ringMax", func() {
			mon := &memoryMonitor{ringMax: 2}
			for i := int64(1); i <= 5; i++ {
				mon.store(memorySample{Components: []componentMemory{{Name: "g", CurrentBytes: i}}})
			}
			pts := mon.componentSeries("g", 0)
			Expect(pts).To(HaveLen(2))
			Expect(pts[1].CurrentBytes).To(Equal(int64(5)))
		})
	})

	Describe("persistence", func() {
		It("writes current.json and an NDJSON line, then prunes old files", func() {
			dir := GinkgoT().TempDir()
			p := newMemoryPersister(dir, 72*time.Hour, nil)

			now := time.Date(2026, 6, 30, 5, 36, 50, 0, time.UTC)
			sample := memorySample{TS: now.Format(time.RFC3339), Mode: MemoryModeHost,
				Components: []componentMemory{{Name: "gateway", CurrentBytes: 123}}}
			p.write(sample, now)

			cur, err := os.ReadFile(filepath.Join(dir, "current.json"))
			Expect(err).NotTo(HaveOccurred())
			var round memorySample
			Expect(json.Unmarshal(cur, &round)).To(Succeed())
			Expect(round.Components[0].CurrentBytes).To(Equal(int64(123)))

			ndjson := filepath.Join(dir, "samples-2026-06-30.ndjson")
			data, err := os.ReadFile(ndjson)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring(`"gateway"`))

			// An old day file beyond retention is pruned on the next prune.
			old := filepath.Join(dir, "samples-2026-06-20.ndjson")
			Expect(os.WriteFile(old, []byte("{}\n"), 0644)).To(Succeed())
			p.pruneExpired(now)
			_, err = os.Stat(old)
			Expect(os.IsNotExist(err)).To(BeTrue())
			// Today's file survives.
			_, err = os.Stat(ndjson)
			Expect(err).NotTo(HaveOccurred())
		})

		It("never panics with a nil persister", func() {
			var p *memoryPersister
			Expect(func() { p.write(memorySample{}, time.Now()); p.pruneExpired(time.Now()) }).NotTo(Panic())
		})
	})

	Describe("incident capture", func() {
		newMon := func(dir string) *memoryMonitor {
			return &memoryMonitor{
				mode:            MemoryModeHost,
				ringMax:         8,
				incidentSamples: 3,
				now:             time.Now,
				persist:         newMemoryPersister(filepath.Join(dir, "memory"), 72*time.Hour, nil),
			}
		}

		It("snapshots the last N samples on an abnormal exit", func() {
			dir := GinkgoT().TempDir()
			mon := newMon(dir)
			for i := int64(1); i <= 5; i++ {
				mon.store(memorySample{
					TS:         time.Unix(i, 0).UTC().Format(time.RFC3339),
					Components: []componentMemory{{Name: "gateway", CurrentBytes: i * 100}},
				})
			}
			mon.captureIncident("gateway", memIncidentChildExit, "child exited after 3s")

			incs := mon.listIncidents()
			Expect(incs).To(HaveLen(1))
			Expect(incs[0].Kind).To(Equal(memIncidentChildExit))
			Expect(incs[0].Component).To(Equal("gateway"))
			Expect(incs[0].Reason).To(ContainSubstring("child exited"))

			// The file holds exactly incident_samples (3) most-recent samples.
			raw, err := os.ReadFile(filepath.Join(dir, "memory", "incidents", incs[0].File))
			Expect(err).NotTo(HaveOccurred())
			var inc memoryIncident
			Expect(json.Unmarshal(raw, &inc)).To(Succeed())
			Expect(inc.Samples).To(HaveLen(3))
			Expect(inc.Samples[2].Components[0].CurrentBytes).To(Equal(int64(500)))
		})

		It("records the component's last memory event for the snapshot", func() {
			dir := GinkgoT().TempDir()
			mon := newMon(dir)
			mon.store(memorySample{Components: []componentMemory{{Name: "gateway", CurrentBytes: 1}}})
			mon.captureIncident("gateway", memIncidentChildExit, "boom")

			e, ok := mon.componentLastEvent("gateway")
			Expect(ok).To(BeTrue())
			Expect(e.Kind).To(Equal(memIncidentChildExit))
			Expect(e.String()).To(ContainSubstring(memIncidentChildExit))

			snap := ComponentSnapshot{Name: "gateway"}
			mon.enrichSnapshot(&snap)
			Expect(snap.MemoryLastEvent).To(ContainSubstring(memIncidentChildExit))
		})

		It("is a safe no-op on a nil monitor", func() {
			var mon *memoryMonitor
			Expect(func() { mon.captureIncident("x", memIncidentChildExit, "r") }).NotTo(Panic())
			Expect(mon.listIncidents()).To(BeNil())
		})
	})

	Describe("humanBytes", func() {
		It("formats binary units and a dash for zero", func() {
			Expect(humanBytes(0)).To(Equal("—"))
			Expect(humanBytes(512)).To(Equal("512 B"))
			Expect(humanBytes(1024)).To(Equal("1.0 KiB"))
			Expect(humanBytes(168 << 20)).To(Equal("168.0 MiB"))
		})
	})

	Describe("config validation", func() {
		on := true
		base := func() *Config {
			return &Config{
				Memory:     &MemoryConfig{Enabled: &on, SupervisorReserve: 0.08, CacheHeadroom: 0.10},
				Components: []ComponentConfig{{Name: "a", Command: "x", Port: 1, Memory: &ComponentMemoryConfig{Share: 0.4}}},
			}
		}
		It("accepts shares summing to <= 1", func() {
			Expect(base().Validate()).To(Succeed())
		})
		It("rejects over-subscribed shares", func() {
			cfg := base()
			cfg.Components[0].Memory.Share = 0.7
			cfg.Components = append(cfg.Components, ComponentConfig{Name: "b", Command: "x", Port: 2, Memory: &ComponentMemoryConfig{Share: 0.5}})
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("sum to <= 1")))
		})
		It("rejects reserves that leave no budget", func() {
			cfg := base()
			cfg.Memory.SupervisorReserve = 0.6
			cfg.Memory.CacheHeadroom = 0.6
			Expect(cfg.Validate()).To(MatchError(ContainSubstring("< 1")))
		})
	})
})
