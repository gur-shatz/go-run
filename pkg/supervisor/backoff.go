package supervisor

import (
	"math/rand/v2"
	"time"
)

// Backoff implements exponential restart delay with jitter:
//
//	min(base * 2^n, cap) + rand(0, base)
//
// n increments on every Next() call. Reset() zeroes n; the caller invokes it
// once the child has run continuously for at least stability_time.
type Backoff struct {
	Base time.Duration
	Cap  time.Duration

	n int

	// Rand is the source of jitter. Zero value uses math/rand/v2's global.
	Rand func() float64
}

// NewBackoff returns a Backoff with the spec defaults (base=1s, cap=60s).
func NewBackoff() *Backoff {
	return &Backoff{Base: time.Second, Cap: 60 * time.Second}
}

// Next returns the next delay and advances the internal counter.
func (this *Backoff) Next() time.Duration {
	delay := this.Base << this.n
	if delay <= 0 || delay > this.Cap {
		delay = this.Cap
	}
	this.n++

	jitter := this.randFloat()
	return delay + time.Duration(jitter*float64(this.Base))
}

// Reset sets the counter back to zero. The next Next() will return ~Base.
func (this *Backoff) Reset() { this.n = 0 }

func (this *Backoff) randFloat() float64 {
	if this.Rand != nil {
		return this.Rand()
	}
	return rand.Float64()
}
