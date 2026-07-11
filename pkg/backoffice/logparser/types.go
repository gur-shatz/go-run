// Package logparser contains parsers for the backoffice log viewer.
package logparser

import (
	"regexp"
	"time"
)

// Entry is one parsed log line.
type Entry struct {
	Stream     string            `json:"stream,omitempty"`
	Segment    string            `json:"segment,omitempty"`
	Line       int64             `json:"line"`
	Offset     int64             `json:"offset"`
	NextOffset int64             `json:"next_offset"`
	Time       *time.Time        `json:"time,omitempty"`
	Level      string            `json:"level,omitempty"`
	Logger     string            `json:"logger,omitempty"`
	Caller     string            `json:"caller,omitempty"`
	Message    string            `json:"message"`
	Fields     map[string]string `json:"fields,omitempty"`
	Raw        string            `json:"raw"`
	Truncated  bool              `json:"truncated,omitempty"`
	Match      bool              `json:"match,omitempty"`
	ContextTop bool              `json:"context_top,omitempty"`
	ContextBot bool              `json:"context_bottom,omitempty"`
}

// LineMeta carries file-position metadata for a parsed line.
type LineMeta struct {
	Stream     string
	Segment    string
	Line       int64
	Offset     int64
	NextOffset int64
	Truncated  bool
}

// Parser parses and filters decoded log lines.
type Parser interface {
	ParseLine(line []byte, meta LineMeta) (Entry, bool)
	Match(entry Entry, filter Filter) bool
}

// LineClassifier lets parsers opt into multiline entry folding. Lines that do
// not start a new entry are appended to the previous entry before ParseLine.
type LineClassifier interface {
	StartsEntryLine(line []byte) bool
	IgnoreLine(line []byte) bool
}

// StreamLineClassifier lets a parser classify physical lines with stream
// context. It is useful for parser multiplexers where different streams use
// different line formats.
type StreamLineClassifier interface {
	StartsEntryLineForStream(stream string, line []byte) bool
	IgnoreLineForStream(stream string, line []byte) bool
}

// Filter describes server-side filtering.
type Filter struct {
	Text     string
	Regex    string
	Level    []string
	Logger   []string
	Since    *time.Time
	Until    *time.Time
	Fields   map[string]string
	CaseFold bool
	Before   int
	After    int
	regex    *regexp.Regexp
}
