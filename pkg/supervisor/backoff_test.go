package supervisor_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("Backoff", func() {
	noJitter := func() float64 { return 0 }

	It("starts at base and doubles each step", func() {
		b := &supervisor.Backoff{Base: time.Second, Cap: time.Minute, Rand: noJitter}
		Expect(b.Next()).To(Equal(1 * time.Second))
		Expect(b.Next()).To(Equal(2 * time.Second))
		Expect(b.Next()).To(Equal(4 * time.Second))
		Expect(b.Next()).To(Equal(8 * time.Second))
	})

	It("caps the exponential at cap", func() {
		b := &supervisor.Backoff{Base: time.Second, Cap: 10 * time.Second, Rand: noJitter}
		var last time.Duration
		for range 10 {
			last = b.Next()
		}
		Expect(last).To(Equal(10 * time.Second))
	})

	It("adds jitter up to base on top of the exponential", func() {
		b := &supervisor.Backoff{Base: time.Second, Cap: time.Minute, Rand: func() float64 { return 0.5 }}
		Expect(b.Next()).To(Equal(1*time.Second + 500*time.Millisecond))
	})

	It("resets to base after Reset", func() {
		b := &supervisor.Backoff{Base: time.Second, Cap: time.Minute, Rand: noJitter}
		_ = b.Next()
		_ = b.Next()
		_ = b.Next()
		b.Reset()
		Expect(b.Next()).To(Equal(1 * time.Second))
	})
})
