// Package logviewer exposes log files below a configured root as a small
// backoffice HTTP application.
package logviewer

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/gur-shatz/go-run/pkg/chiutil"
)

const (
	defaultLimit        = 200
	defaultMaxLimit     = 1000
	defaultMaxLineBytes = 1 << 20
	defaultBlockBytes   = 64 << 10
)

// Options configures a Viewer.
type Options struct {
	Root      string
	Parser    Parser
	Recursive bool
	// Prefixes limits the viewer to files whose base name starts with any prefix.
	// Prefix matching is case-sensitive and is combined with Globs.
	Prefixes     []string
	Globs        []string
	MaxLimit     int
	DefaultLimit int
	MaxLineBytes int
	BlockBytes   int
}

// Viewer serves the log viewer API and HTML preview.
type Viewer struct {
	opts   Options
	parser Parser
	router http.Handler
}

// IndexedHandler is implemented by Viewer for RouteFolder integration.
type IndexedHandler interface {
	http.Handler
	Index() chiutil.FolderIndex
}

// Stream is a logical log stream made from one or more physical segments.
type Stream struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Segments []Segment `json:"segments_detail,omitempty"`
}

// Segment is one physical log file within a logical stream.
type Segment struct {
	Path    string    `json:"path"`
	Index   int       `json:"index"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

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

// Query describes one page request.
type Query struct {
	Cursor    Cursor
	Limit     int
	Direction Direction
	Filter    Filter
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

// Direction controls page scan direction.
type Direction string

const (
	Forward  Direction = "forward"
	Backward Direction = "backward"
)

// Cursor is the internal cursor representation. It is encoded before being
// returned to clients.
type Cursor struct {
	StreamID     string `json:"stream"`
	SegmentIndex int    `json:"segment"`
	Offset       int64  `json:"offset"`
	Line         int64  `json:"line,omitempty"`
}

// Page is returned by tail, page, and search endpoints.
type Page struct {
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"next_cursor,omitempty"`
	PrevCursor string  `json:"prev_cursor,omitempty"`
	EOF        bool    `json:"eof"`
	BOF        bool    `json:"bof"`
	Range      Range   `json:"range"`
}

// Range describes where a page sits inside the logical stream. Byte positions
// are exact and cheap to compute; total line counts are intentionally omitted
// until an index exists.
type Range struct {
	StreamID      string `json:"stream"`
	SegmentCount  int    `json:"segment_count"`
	TotalBytes    int64  `json:"total_bytes"`
	EntryCount    int    `json:"entry_count"`
	StartSegment  int    `json:"start_segment"`
	StartPath     string `json:"start_path,omitempty"`
	StartOffset   int64  `json:"start_offset"`
	EndSegment    int    `json:"end_segment"`
	EndPath       string `json:"end_path,omitempty"`
	EndOffset     int64  `json:"end_offset"`
	StartAbsolute int64  `json:"start_absolute"`
	EndAbsolute   int64  `json:"end_absolute"`
}

var (
	errStreamNotFound   = errors.New("stream not found")
	errInvalidCursor    = errors.New("invalid cursor")
	errInvalidDirection = errors.New("invalid direction")
	errInvalidSegment   = errors.New("invalid segment")
	errInvalidLimit     = errors.New("invalid limit")
	errInvalidFilter    = errors.New("invalid filter")
)
