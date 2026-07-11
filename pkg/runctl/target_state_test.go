package runctl

import (
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("target phase state", func() {
	newRunTarget := func() *target {
		t := &target{name: "t", hasBuild: true, hasTest: true, hasRun: true, state: StateIdle}
		return t
	}

	Describe("markPhaseDone", func() {
		It("returns to running after a test-only trigger while the process is up", func() {
			t := newRunTarget()
			t.markRunStart(1234, time.Now())
			Expect(t.state).To(Equal(StateRunning))

			t.markPhaseStart("test", time.Now())
			Expect(t.state).To(Equal(StateStarting))
			Expect(t.currentStage).To(Equal("test"))

			t.markPhaseDone("test", 2*time.Second, nil, true)
			Expect(t.state).To(Equal(StateRunning))
			Expect(t.currentStage).To(Equal("run"))
			Expect(t.lastTestResult).To(Equal("success"))
			Expect(t.testCount).To(Equal(1))
		})

		It("stays starting during an initial start sequence (no process yet)", func() {
			t := newRunTarget()

			t.markPhaseStart("build", time.Now())
			t.markPhaseDone("build", time.Second, nil, true)
			Expect(t.state).To(Equal(StateStarting))
			Expect(t.currentStage).To(Equal("build"))

			t.markPhaseStart("test", time.Now())
			t.markPhaseDone("test", time.Second, nil, true)
			Expect(t.state).To(Equal(StateStarting))
			Expect(t.currentStage).To(Equal("test"))

			t.markRunStart(1234, time.Now())
			Expect(t.state).To(Equal(StateRunning))
			Expect(t.currentStage).To(Equal("run"))
		})

		It("returns build-only targets to idle after each successful phase", func() {
			t := &target{name: "t", hasBuild: true, hasTest: false, hasRun: false, state: StateIdle}

			t.markPhaseStart("build", time.Now())
			t.markPhaseDone("build", time.Second, nil, true)
			Expect(t.state).To(Equal(StateIdle))
			Expect(t.currentStage).To(Equal(""))

			t.markPhaseStart("test", time.Now())
			t.markPhaseDone("test", 0, nil, false)
			Expect(t.state).To(Equal(StateIdle))
			Expect(t.currentStage).To(Equal(""))
		})

		It("keeps the error state when a phase fails", func() {
			t := newRunTarget()
			t.markRunStart(1234, time.Now())

			t.markPhaseStart("test", time.Now())
			t.markPhaseDone("test", time.Second, errors.New("boom"), true)
			Expect(t.state).To(Equal(StateError))
			Expect(t.currentStage).To(Equal("test"))
			Expect(t.lastTestResult).To(Equal("failed"))
		})
	})
})
