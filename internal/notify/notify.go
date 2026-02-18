package notify

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gur-shatz/go-run/internal/sumfile"
)

// Event types emitted as stdout protocol lines.
const (
	EventStarted     = "started"
	EventRebuilt     = "rebuilt"
	EventBuildFailed = "build_failed"
	EventChanged     = "changed"
	EventStopping    = "stopping"
)

// Event is the JSON payload for a protocol line.
type Event struct {
	Type        string   `json:"type"`
	PID         int      `json:"pid,omitempty"`
	BuildTimeMs int64    `json:"build_time_ms,omitempty"`
	Error       string   `json:"error,omitempty"`
	Added       []string `json:"added,omitempty"`
	Modified    []string `json:"modified,omitempty"`
	Removed     []string `json:"removed,omitempty"`
}

// Notifier emits structured protocol lines to a writer.
type Notifier struct {
	w io.Writer
}

// New creates a Notifier that writes to stdout.
func New() *Notifier {
	return &Notifier{w: os.Stdout}
}

// NewWithWriter creates a Notifier that writes to the given writer (for testing).
func NewWithWriter(w io.Writer) *Notifier {
	return &Notifier{w: w}
}

// Started emits a started event after the initial build and run.
func (this *Notifier) Started(pid int, buildTime time.Duration) {
	this.emit(Event{
		Type:        EventStarted,
		PID:         pid,
		BuildTimeMs: buildTime.Milliseconds(),
	})
}

// Rebuilt emits a rebuilt event after a successful rebuild and restart.
func (this *Notifier) Rebuilt(pid int, buildTime time.Duration, changes sumfile.ChangeSet) {
	this.emit(Event{
		Type:        EventRebuilt,
		PID:         pid,
		BuildTimeMs: buildTime.Milliseconds(),
		Added:       changes.Added,
		Modified:    changes.Modified,
		Removed:     changes.Removed,
	})
}

// BuildFailed emits a build_failed event.
func (this *Notifier) BuildFailed(err error, changes sumfile.ChangeSet) {
	this.emit(Event{
		Type:     EventBuildFailed,
		Error:    err.Error(),
		Added:    changes.Added,
		Modified: changes.Modified,
		Removed:  changes.Removed,
	})
}

// Changed emits a changed event when file changes are detected (before rebuild).
func (this *Notifier) Changed(changes sumfile.ChangeSet) {
	this.emit(Event{
		Type:     EventChanged,
		Added:    changes.Added,
		Modified: changes.Modified,
		Removed:  changes.Removed,
	})
}

// Stopping emits a stopping event on shutdown.
func (this *Notifier) Stopping() {
	this.emit(Event{
		Type: EventStopping,
	})
}

func (this *Notifier) emit(event Event) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintf(this.w, "[gorun:%s] %s\n", event.Type, data)
}
