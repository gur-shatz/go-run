// subprocess.go provides a library for running gorun as a subprocess
// and receiving structured callbacks on rebuild events.
//
// Usage:
//
//	cmd := gorun.Command("./cmd/server", "-port", "8080")
//	cmd.OnEvent = func(event gorun.Event) {
//	    switch event.Type {
//	    case gorun.EventStarted:
//	        log.Printf("started pid=%d build=%dms", event.PID, event.BuildTimeMs)
//	    case gorun.EventRebuilt:
//	        log.Printf("rebuilt pid=%d", event.PID)
//	    case gorun.EventBuildFailed:
//	        log.Printf("build failed: %s", event.Error)
//	    }
//	}
//	cmd.Start()
//	defer cmd.Stop()
//	cmd.Wait()
package gorun

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Event types matching the gorun stdout protocol.
const (
	EventStarted     = "started"
	EventRebuilt     = "rebuilt"
	EventBuildFailed = "build_failed"
	EventChanged     = "changed"
	EventStopping    = "stopping"
)

// Event is a parsed gorun protocol event.
type Event struct {
	Type        string   `json:"type"`
	PID         int      `json:"pid,omitempty"`
	BuildTimeMs int64    `json:"build_time_ms,omitempty"`
	Error       string   `json:"error,omitempty"`
	Added       []string `json:"added,omitempty"`
	Modified    []string `json:"modified,omitempty"`
	Removed     []string `json:"removed,omitempty"`
}

// Cmd represents a gorun subprocess. Create one with Command.
type Cmd struct {
	// OnEvent is called for each gorun protocol event.
	// Called from a goroutine — must be safe for concurrent use.
	OnEvent func(Event)

	// Stdout receives the child process output (non-protocol lines).
	// Defaults to os.Stdout.
	Stdout io.Writer

	// Stderr receives gorun and child stderr output.
	// Defaults to os.Stderr.
	Stderr io.Writer

	// Dir sets the working directory. Empty means current directory.
	Dir string

	// GorunBin is the path to the gorun binary. Defaults to "gorun".
	GorunBin string

	// Poll sets the poll interval. Zero means gorun default.
	Poll time.Duration

	// Debounce sets the debounce window. Zero means gorun default.
	Debounce time.Duration

	// Verbose enables gorun verbose output.
	Verbose bool

	// BuildFlags are passed to "go build" (e.g., "-race", "-tags=integration").
	BuildFlags []string

	buildTarget string
	appArgs     []string

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan error
}

// Command creates a new Cmd that will run gorun with the given build target
// and application arguments.
func Command(buildTarget string, appArgs ...string) *Cmd {
	return &Cmd{
		buildTarget: buildTarget,
		appArgs:     appArgs,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		GorunBin:    "gorun",
		done:        make(chan error, 1),
	}
}

// Start launches the gorun subprocess. Returns an error if the process
// cannot be started.
func (this *Cmd) Start() error {
	args := this.buildArgs()

	this.mu.Lock()
	this.cmd = exec.Command(this.GorunBin, args...)
	this.cmd.Stderr = this.Stderr
	this.cmd.Stdin = os.Stdin
	if this.Dir != "" {
		this.cmd.Dir = this.Dir
	}

	stdout, err := this.cmd.StdoutPipe()
	if err != nil {
		this.mu.Unlock()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := this.cmd.Start(); err != nil {
		this.mu.Unlock()
		return fmt.Errorf("start gorun: %w", err)
	}
	this.mu.Unlock()

	go this.scanOutput(stdout)
	go func() {
		this.done <- this.cmd.Wait()
	}()

	return nil
}

// Wait blocks until the gorun process exits. Returns the exit error, if any.
func (this *Cmd) Wait() error {
	return <-this.done
}

// Stop sends SIGINT to the gorun process for a clean shutdown (gorun will
// forward the signal to the child and clean up). Returns after the process
// exits or an error if the signal fails.
func (this *Cmd) Stop() error {
	this.mu.Lock()
	cmd := this.cmd
	this.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("signal gorun: %w", err)
	}

	return this.Wait()
}

func (this *Cmd) buildArgs() []string {
	var args []string
	if this.Poll > 0 {
		args = append(args, "--poll", this.Poll.String())
	}
	if this.Debounce > 0 {
		args = append(args, "--debounce", this.Debounce.String())
	}
	if this.Verbose {
		args = append(args, "-v")
	}
	if len(this.BuildFlags) > 0 {
		args = append(args, "--")
		args = append(args, this.BuildFlags...)
	}
	args = append(args, this.buildTarget)
	args = append(args, this.appArgs...)
	return args
}

func (this *Cmd) scanOutput(r io.Reader) {
	ScanOutput(r, this.Stdout, this.OnEvent)
}

// ScanOutput reads lines from r, dispatches gorun protocol lines to onEvent,
// and writes all other lines (child process output) to childOut.
func ScanOutput(r io.Reader, childOut io.Writer, onEvent func(Event)) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if event, ok := ParseProtocolLine(line); ok {
			if onEvent != nil {
				onEvent(event)
			}
		} else {
			fmt.Fprintln(childOut, line)
		}
	}
}

// ParseProtocolLine parses a "[gorun:type] {json}" line.
// Returns the event and true if the line is a protocol line.
func ParseProtocolLine(line string) (Event, bool) {
	if !strings.HasPrefix(line, "[gorun:") {
		return Event{}, false
	}
	rest, found := strings.CutPrefix(line, "[gorun:")
	if !found {
		return Event{}, false
	}
	_, jsonPayload, found := strings.Cut(rest, "] ")
	if !found {
		return Event{}, false
	}
	var event Event
	if err := json.Unmarshal([]byte(jsonPayload), &event); err != nil {
		return Event{}, false
	}
	return event, true
}
