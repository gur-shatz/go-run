package log

import (
	"fmt"
	"os"

	"github.com/gur-shatz/go-run/internal/color"
	"github.com/gur-shatz/go-run/internal/sumfile"
)

// Logger is an instance-based logger with its own prefix and verbosity.
type Logger struct {
	prefix  string
	verbose bool
}

// New creates a new Logger with the given prefix and verbosity.
func New(prefix string, verbose bool) *Logger {
	return &Logger{prefix: prefix, verbose: verbose}
}

// Error prints a red error message to stderr.
func (this *Logger) Error(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%s %s %s\n", this.prefix, color.Red("Error:"), msg)
}

// Warn prints a yellow warning message to stdout.
func (this *Logger) Warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(this.prefix + " " + color.Yellow(msg))
}

// Success prints a green success message to stdout.
func (this *Logger) Success(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(this.prefix + " " + color.Green(msg))
}

// Status prints a bold status message to stdout.
func (this *Logger) Status(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(color.Bold(this.prefix + " " + msg))
}

// Verbose prints a dim message to stdout, only if verbose mode is enabled.
func (this *Logger) Verbose(format string, args ...any) {
	if !this.verbose {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Println(color.Dim(this.prefix + " " + msg))
}

// Tick prints a heartbeat dot — green if ok, red if not. No newline.
func (this *Logger) Tick(ok bool) {
	if ok {
		fmt.Print(color.Green("."))
	} else {
		fmt.Print(color.Red("."))
	}
	os.Stdout.Sync()
}

// Change prints a changeset with a cyan header and dim file paths.
func (this *Logger) Change(changes sumfile.ChangeSet) {
	fmt.Println(this.prefix + " " + color.Cyan("Changes detected:"))
	for _, f := range changes.Modified {
		fmt.Println(color.Dim("  modified: " + f))
	}
	for _, f := range changes.Added {
		fmt.Println(color.Dim("  added:    " + f))
	}
	for _, f := range changes.Removed {
		fmt.Println(color.Dim("  removed:  " + f))
	}
}

// --- Global convenience functions for standalone (single-target) use ---

var defaultLogger = &Logger{prefix: "[gorun]"}

// Init initializes the global logger. Must be called before any other global log function.
func Init(v bool) {
	defaultLogger.verbose = v
	color.Init()
}

// SetPrefix changes the global log prefix (default "[gorun]").
func SetPrefix(p string) {
	defaultLogger.prefix = p
}

func Error(format string, args ...any)   { defaultLogger.Error(format, args...) }
func Warn(format string, args ...any)    { defaultLogger.Warn(format, args...) }
func Success(format string, args ...any) { defaultLogger.Success(format, args...) }
func Status(format string, args ...any)  { defaultLogger.Status(format, args...) }
func Verbose(format string, args ...any) { defaultLogger.Verbose(format, args...) }
func Tick(ok bool)                       { defaultLogger.Tick(ok) }
func Change(changes sumfile.ChangeSet)   { defaultLogger.Change(changes) }
