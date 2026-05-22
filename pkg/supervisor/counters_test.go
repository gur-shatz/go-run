package supervisor_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("Counters", func() {
	It("counts an exit with uptime < crash_window as a fast crash", func() {
		c := supervisor.NewCounters(10*time.Second, 3, 2)
		launched := time.Now()
		c.OnExit(launched, launched.Add(2*time.Second))
		Expect(c.FastCrashes).To(Equal(1))
	})

	It("does not count an exit after crash_window", func() {
		c := supervisor.NewCounters(10*time.Second, 3, 2)
		launched := time.Now()
		c.OnExit(launched, launched.Add(15*time.Second))
		Expect(c.FastCrashes).To(BeZero())
	})

	It("trips ShouldReject once fast crashes hit the threshold", func() {
		c := supervisor.NewCounters(10*time.Second, 3, 2)
		now := time.Now()
		Expect(c.ShouldReject()).To(BeFalse())
		c.OnExit(now, now.Add(time.Second))
		c.OnExit(now, now.Add(time.Second))
		Expect(c.ShouldReject()).To(BeFalse())
		c.OnExit(now, now.Add(time.Second))
		Expect(c.ShouldReject()).To(BeTrue())
	})

	It("trips ShouldReject once exec failures hit their (lower) threshold", func() {
		c := supervisor.NewCounters(10*time.Second, 3, 2)
		c.OnExecFailure()
		Expect(c.ShouldReject()).To(BeFalse())
		c.OnExecFailure()
		Expect(c.ShouldReject()).To(BeTrue())
	})

	It("resets both counters on Reset()", func() {
		c := supervisor.NewCounters(10*time.Second, 3, 2)
		now := time.Now()
		c.OnExit(now, now.Add(time.Second))
		c.OnExecFailure()
		c.Reset()
		Expect(c.FastCrashes).To(BeZero())
		Expect(c.ExecFailures).To(BeZero())
	})
})
