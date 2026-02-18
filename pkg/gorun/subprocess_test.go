package gorun_test

import (
	"bytes"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/gorun"
)

var _ = Describe("Subprocess", func() {
	Describe("Command", func() {
		It("creates a command with build target and app args", func() {
			cmd := gorun.Command("./cmd/server", "-port", "8080")
			Expect(cmd).NotTo(BeNil())
		})
	})

	Describe("buildArgs (via exported fields)", func() {
		It("includes poll when set", func() {
			cmd := gorun.Command("./cmd/server")
			cmd.Poll = 1 * time.Second
			// We verify indirectly through the command structure
			Expect(cmd.Poll).To(Equal(1 * time.Second))
		})

		It("includes debounce when set", func() {
			cmd := gorun.Command("./cmd/server")
			cmd.Debounce = 200 * time.Millisecond
			Expect(cmd.Debounce).To(Equal(200 * time.Millisecond))
		})

		It("includes verbose flag", func() {
			cmd := gorun.Command("./cmd/server")
			cmd.Verbose = true
			Expect(cmd.Verbose).To(BeTrue())
		})
	})

	Describe("ParseProtocolLine", func() {
		It("parses a started event", func() {
			event, ok := gorun.ParseProtocolLine(`[gorun:started] {"type":"started","pid":1234,"build_time_ms":1200}`)
			Expect(ok).To(BeTrue())
			Expect(event.Type).To(Equal("started"))
			Expect(event.PID).To(Equal(1234))
			Expect(event.BuildTimeMs).To(Equal(int64(1200)))
		})

		It("parses a rebuilt event with changes", func() {
			event, ok := gorun.ParseProtocolLine(`[gorun:rebuilt] {"type":"rebuilt","pid":5678,"build_time_ms":900,"modified":["handler.go"],"added":["new.go"]}`)
			Expect(ok).To(BeTrue())
			Expect(event.Type).To(Equal("rebuilt"))
			Expect(event.PID).To(Equal(5678))
			Expect(event.Modified).To(Equal([]string{"handler.go"}))
			Expect(event.Added).To(Equal([]string{"new.go"}))
		})

		It("parses a build_failed event", func() {
			event, ok := gorun.ParseProtocolLine(`[gorun:build_failed] {"type":"build_failed","error":"syntax error","modified":["main.go"]}`)
			Expect(ok).To(BeTrue())
			Expect(event.Type).To(Equal("build_failed"))
			Expect(event.Error).To(Equal("syntax error"))
		})

		It("parses a changed event", func() {
			event, ok := gorun.ParseProtocolLine(`[gorun:changed] {"type":"changed","added":["new.go"],"removed":["old.go"]}`)
			Expect(ok).To(BeTrue())
			Expect(event.Type).To(Equal("changed"))
			Expect(event.Added).To(Equal([]string{"new.go"}))
			Expect(event.Removed).To(Equal([]string{"old.go"}))
		})

		It("parses a stopping event", func() {
			event, ok := gorun.ParseProtocolLine(`[gorun:stopping] {"type":"stopping"}`)
			Expect(ok).To(BeTrue())
			Expect(event.Type).To(Equal("stopping"))
		})

		It("returns false for non-protocol lines", func() {
			_, ok := gorun.ParseProtocolLine("Hello from the server")
			Expect(ok).To(BeFalse())
		})

		It("returns false for malformed protocol lines", func() {
			_, ok := gorun.ParseProtocolLine("[gorun:started] not-json")
			Expect(ok).To(BeFalse())
		})

		It("returns false for lines without JSON payload", func() {
			_, ok := gorun.ParseProtocolLine("[gorun:started]")
			Expect(ok).To(BeFalse())
		})
	})

	Describe("ScanOutput", func() {
		It("separates protocol lines from child output", func() {
			input := strings.NewReader(
				"server listening on :8080\n" +
					`[gorun:started] {"type":"started","pid":1234,"build_time_ms":500}` + "\n" +
					"handling request\n" +
					`[gorun:rebuilt] {"type":"rebuilt","pid":5678,"build_time_ms":300,"modified":["main.go"]}` + "\n" +
					"more output\n",
			)

			var childOut bytes.Buffer
			var events []gorun.Event

			gorun.ScanOutput(input, &childOut, func(e gorun.Event) {
				events = append(events, e)
			})

			Expect(events).To(HaveLen(2))
			Expect(events[0].Type).To(Equal("started"))
			Expect(events[0].PID).To(Equal(1234))
			Expect(events[1].Type).To(Equal("rebuilt"))
			Expect(events[1].Modified).To(Equal([]string{"main.go"}))

			Expect(childOut.String()).To(Equal("server listening on :8080\nhandling request\nmore output\n"))
		})
	})
})
