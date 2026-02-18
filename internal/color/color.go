package color

import (
	"os"

	"golang.org/x/term"
)

var enabled bool

// Init detects whether stderr is a TTY and enables colors accordingly.
func Init() {
	enabled = term.IsTerminal(int(os.Stderr.Fd()))
}

func wrap(code, s string) string {
	if !enabled {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func Red(s string) string    { return wrap("31", s) }
func Yellow(s string) string { return wrap("33", s) }
func Green(s string) string  { return wrap("32", s) }
func Bold(s string) string   { return wrap("1", s) }
func Dim(s string) string    { return wrap("2", s) }
func Cyan(s string) string   { return wrap("36", s) }
