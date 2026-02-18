package notify_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/notify"
	"github.com/gur-shatz/go-run/internal/sumfile"
)

var _ = Describe("Notify", func() {
	var (
		buf *bytes.Buffer
		n   *notify.Notifier
	)

	BeforeEach(func() {
		buf = &bytes.Buffer{}
		n = notify.NewWithWriter(buf)
	})

	parseEvent := func(line string) notify.Event {
		// Extract JSON from "[gorun:type] {json}"
		idx := strings.Index(line, "] ")
		Expect(idx).To(BeNumerically(">", 0))
		jsonStr := line[idx+2:]
		var event notify.Event
		Expect(json.Unmarshal([]byte(jsonStr), &event)).To(Succeed())
		return event
	}

	Describe("Started", func() {
		It("emits a started event with PID and build time", func() {
			n.Started(1234, 1200*time.Millisecond)

			line := strings.TrimSpace(buf.String())
			Expect(line).To(HavePrefix("[gorun:started]"))

			event := parseEvent(line)
			Expect(event.Type).To(Equal("started"))
			Expect(event.PID).To(Equal(1234))
			Expect(event.BuildTimeMs).To(Equal(int64(1200)))
		})
	})

	Describe("Rebuilt", func() {
		It("emits a rebuilt event with PID, build time, and changes", func() {
			changes := sumfile.ChangeSet{
				Modified: []string{"internal/handler.go"},
				Added:    []string{"internal/middleware.go"},
			}
			n.Rebuilt(5678, 900*time.Millisecond, changes)

			line := strings.TrimSpace(buf.String())
			Expect(line).To(HavePrefix("[gorun:rebuilt]"))

			event := parseEvent(line)
			Expect(event.Type).To(Equal("rebuilt"))
			Expect(event.PID).To(Equal(5678))
			Expect(event.BuildTimeMs).To(Equal(int64(900)))
			Expect(event.Modified).To(Equal([]string{"internal/handler.go"}))
			Expect(event.Added).To(Equal([]string{"internal/middleware.go"}))
		})
	})

	Describe("BuildFailed", func() {
		It("emits a build_failed event with error and changes", func() {
			changes := sumfile.ChangeSet{
				Modified: []string{"main.go"},
			}
			n.BuildFailed(fmt.Errorf("syntax error"), changes)

			line := strings.TrimSpace(buf.String())
			Expect(line).To(HavePrefix("[gorun:build_failed]"))

			event := parseEvent(line)
			Expect(event.Type).To(Equal("build_failed"))
			Expect(event.Error).To(Equal("syntax error"))
			Expect(event.Modified).To(Equal([]string{"main.go"}))
		})
	})

	Describe("Changed", func() {
		It("emits a changed event with file lists", func() {
			changes := sumfile.ChangeSet{
				Added:    []string{"new.go"},
				Removed:  []string{"old.go"},
			}
			n.Changed(changes)

			line := strings.TrimSpace(buf.String())
			Expect(line).To(HavePrefix("[gorun:changed]"))

			event := parseEvent(line)
			Expect(event.Type).To(Equal("changed"))
			Expect(event.Added).To(Equal([]string{"new.go"}))
			Expect(event.Removed).To(Equal([]string{"old.go"}))
		})
	})

	Describe("Stopping", func() {
		It("emits a stopping event", func() {
			n.Stopping()

			line := strings.TrimSpace(buf.String())
			Expect(line).To(HavePrefix("[gorun:stopping]"))

			event := parseEvent(line)
			Expect(event.Type).To(Equal("stopping"))
		})
	})

	Describe("protocol line format", func() {
		It("follows [gorun:type] JSON format", func() {
			n.Started(1, 100*time.Millisecond)

			line := strings.TrimSpace(buf.String())
			Expect(line).To(MatchRegexp(`^\[gorun:started\] \{.*\}$`))
		})
	})
})
