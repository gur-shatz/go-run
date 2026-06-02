package supervisor

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("statemonitor config", func() {
	It("defaults scrape to enabled when the block is absent", func() {
		var cfg Config
		Expect(cfg.StateMonitor.Scrape.IsEnabled()).To(BeTrue())
	})

	It("honours an explicit scrape disable", func() {
		off := false
		cfg := Config{}
		cfg.StateMonitor.Scrape.Enabled = &off
		Expect(cfg.StateMonitor.Scrape.IsEnabled()).To(BeFalse())

		on := true
		cfg.StateMonitor.Scrape.Enabled = &on
		Expect(cfg.StateMonitor.Scrape.IsEnabled()).To(BeTrue())
	})

	It("applies scrape interval/timeout/expiration defaults", func() {
		cfg := Config{StateDir: "x"}
		cfg.ApplyDefaults()
		Expect(cfg.StateMonitor.Scrape.Interval).To(Equal(15 * time.Second))
		Expect(cfg.StateMonitor.Scrape.Timeout).To(Equal(5 * time.Second))
		Expect(cfg.StateMonitor.Scrape.Expiration).To(Equal(time.Minute))
	})

	It("applies observe defaults only when enabled", func() {
		cfg := Config{StateDir: "x"}
		cfg.ApplyDefaults()
		Expect(cfg.StateMonitor.Observe.IngestInterval).To(BeZero()) // disabled -> untouched

		cfg2 := Config{StateDir: "x"}
		cfg2.StateMonitor.Observe.Enabled = true
		cfg2.ApplyDefaults()
		Expect(cfg2.StateMonitor.Observe.IngestInterval).To(Equal(time.Second))
		Expect(cfg2.StateMonitor.Observe.CacheMB).To(Equal(32))
	})
})
