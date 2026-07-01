package supervisor

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("ByteSize / hardlimit", func() {
	Describe("parseByteSize", func() {
		DescribeTable("parses human sizes with binary units",
			func(in string, want int64) {
				got, err := parseByteSize(in)
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(want))
			},
			Entry("plain bytes", "10485760", int64(10485760)),
			Entry("megabytes", "10m", int64(10*1024*1024)),
			Entry("kilobytes", "512k", int64(512*1024)),
			Entry("gigabytes", "1g", int64(1024*1024*1024)),
			Entry("fractional", "1.5g", int64(1.5*1024*1024*1024)),
			Entry("uppercase with B", "10MB", int64(10*1024*1024)),
			Entry("iB suffix", "10MiB", int64(10*1024*1024)),
			Entry("empty is zero", "", int64(0)),
		)

		It("rejects a non-numeric size", func() {
			_, err := parseByteSize("lots")
			Expect(err).To(HaveOccurred())
		})

		It("rejects a negative size", func() {
			_, err := parseByteSize("-5m")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("UnmarshalYAML on a component memory block", func() {
		It("parses `hardlimit: 10m` into bytes", func() {
			var cm ComponentMemoryConfig
			Expect(yaml.Unmarshal([]byte("hardlimit: 10m\n"), &cm)).To(Succeed())
			Expect(int64(cm.HardLimit)).To(Equal(int64(10 * 1024 * 1024)))
		})

		It("parses a bare integer as raw bytes", func() {
			var cm ComponentMemoryConfig
			Expect(yaml.Unmarshal([]byte("hardlimit: 10485760\n"), &cm)).To(Succeed())
			Expect(int64(cm.HardLimit)).To(Equal(int64(10485760)))
		})
	})

	Describe("deriveBudgets with an absolute hardlimit", func() {
		It("sets the hard limit to the absolute value with no resolved global limit", func() {
			comps := []ComponentConfig{
				{Name: "leaker", Memory: &ComponentMemoryConfig{HardLimit: 50 << 20}},
			}
			m := &MemoryConfig{CacheHeadroom: 0.10}
			budgets := deriveBudgets(0 /* L unresolved */, m, comps)

			b := budgets["leaker"]
			Expect(b.LimitBytes).To(Equal(int64(50 << 20)))
			// Soft band sits ~10% below the hard cap.
			Expect(b.HighBytes).To(BeNumerically("~", int64(float64(50<<20)*0.90), 2))
		})

		It("makes the component budgeted so the enforcer can act", func() {
			budgets := deriveBudgets(0, &MemoryConfig{CacheHeadroom: 0.10},
				[]ComponentConfig{{Name: "leaker", Memory: &ComponentMemoryConfig{HardLimit: 50 << 20}}})
			Expect(anyBudgeted(budgets)).To(BeTrue())
		})

		It("uses an explicit softlimit for the warn band when set", func() {
			comps := []ComponentConfig{
				{Name: "leaker", Memory: &ComponentMemoryConfig{SoftLimit: 30 << 20, HardLimit: 50 << 20}},
			}
			b := deriveBudgets(0, &MemoryConfig{CacheHeadroom: 0.10}, comps)["leaker"]
			Expect(b.HighBytes).To(Equal(int64(30 << 20)))
			Expect(b.LimitBytes).To(Equal(int64(50 << 20)))
		})

		It("derives the warn band from the hard cap when softlimit is unset", func() {
			comps := []ComponentConfig{
				{Name: "leaker", Memory: &ComponentMemoryConfig{HardLimit: 50 << 20}},
			}
			b := deriveBudgets(0, &MemoryConfig{CacheHeadroom: 0.10}, comps)["leaker"]
			Expect(b.HighBytes).To(BeNumerically("~", int64(float64(50<<20)*0.90), 2))
		})
	})
})
