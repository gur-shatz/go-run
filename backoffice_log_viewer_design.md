# Backoffice Log Viewer Design

## Goal

Build a small backoffice application that exposes a filesystem folder of log
files through the existing `backoffice` / `chiutil.RouteFolder` navigation.

The viewer should support:

- browsing log files in a configured directory
- viewing large files without loading them fully into memory
- grep-style filtering implemented in Go, not by shelling out to OS tools
- paged and infinite-scroll access
- structured parsing of log lines
- a useful default parser for console logs with ANSI color markers removed

The first version should be a library package that builds an HTTP route object.
Callers decide where and how to mount it, matching the spirit of
`chiutil.RouteFolder`.

## Proposed Package

Package path:

```text
pkg/backoffice/logviewer
```

Example usage:

```go
bo := backoffice.New()

logs, err := logviewer.New(logviewer.Options{
	Root: "/var/log/my-service",
})
if err != nil {
	return err
}

// Proposed chiutil helper. The caller decides the mount point.
bo.Folder().MountHandler("logs", "Log files", logs)
```

This creates:

```text
/logs/                    HTML app shell and stream listing
/logs/index.json          chiutil folder index
/logs/api/streams         JSON list of logical streams
/logs/api/streams/{id}    stream metadata
/logs/api/streams/{id}/tail newest page
/logs/api/streams/{id}/page cursor-based page
/logs/api/streams/{id}/search cursor-based filtered page
/logs/api/streams/{id}/raw bounded raw byte range
```

The log viewer should not mount itself. It should expose an HTTP handler that
the caller can mount under any parent. The richer UI should be the folder index
preview, not a separate landing page.

## Navigation Experience

The log viewer has two visible levels:

1. Log directory page
2. Log stream page

The directory page lists logical log streams, not necessarily individual
physical files. Rotated files such as these should be grouped as one stream:

```text
gateway.log
gateway.log.1
gateway.log.2
```

The user sees one entry:

```text
gateway.log
```

Selecting it opens the stream page for that logical log. The stream page is the
main application view:

- rows of parsed log entries
- infinite scroll up and down
- tail/latest button
- server-side search
- structured filters
- raw/parsed display toggle
- optional selector for which physical segment matched a row

All meaningful operations are server-backed. The browser does not grep,
paginate, or scan files locally. It only renders pages returned by the server.

### Logical Streams and Physical Segments

A logical stream is a group of related physical files:

```go
type Stream struct {
	ID       string
	Name     string
	Segments []Segment
}

type Segment struct {
	Path    string
	Index   int
	Size    int64
	ModTime time.Time
}
```

Segment order is chronological. For common rotation schemes:

```text
app.log      newest
app.log.1    older
app.log.2    oldest
```

the stream order should be:

```text
app.log.2 -> app.log.1 -> app.log
```

The API cursor must therefore include both stream position and byte position:

```go
type Cursor struct {
	StreamID     string `json:"stream"`
	SegmentIndex int    `json:"segment"`
	Offset       int64  `json:"offset"`
	Line         int64  `json:"line,omitempty"`
}
```

Scrolling across a segment boundary should be seamless. If the user scrolls
older than the beginning of `app.log.1`, the server continues into `app.log.2`.
If the user scrolls newer than the end of `app.log.1`, the server continues
into `app.log`.

### Server-Owned Interactions

Every interaction is an API request:

- initial directory load: `GET /api/streams`
- open a stream: `GET /api/streams/{id}/tail`
- scroll older: `GET /api/streams/{id}/page?direction=backward&cursor=...`
- scroll newer: `GET /api/streams/{id}/page?direction=forward&cursor=...`
- search: `GET /api/streams/{id}/search?q=...`
- continue search older/newer: same search endpoint with cursor and direction
- change filters: new server request with filter params
- raw row context: `GET /api/streams/{id}/raw?...`

The UI may keep a small cache of recently returned pages for smooth scrolling,
but the server remains the source of truth for scanning, matching, and paging.

## Core Concepts

### Log Stream Identity

Streams are addressed by an opaque URL-safe id, not by direct path. Internally
the id maps to one or more paths below `Options.Root`.

Requirements:

- reject paths escaping the root
- ignore directories unless recursive browsing is explicitly enabled
- optionally include glob filtering, for example `*.log`, `*.out`, `*.txt`
- group rotated files into logical streams
- expose stable metadata: name, relative paths, total size, newest mod time

Initial implementation can compute ids from the logical stream name. If later
we need rename stability or hide paths, replace that with a small in-memory
catalog.

### Parsed Line

The viewer works with parsed log entries, but it should preserve raw text.

```go
type Entry struct {
	Stream      string            `json:"stream,omitempty"`
	Segment     string            `json:"segment,omitempty"`
	Line        int64             `json:"line"`
	Offset      int64             `json:"offset"`
	NextOffset  int64             `json:"next_offset"`
	Time        *time.Time        `json:"time,omitempty"`
	Level       string            `json:"level,omitempty"`
	Logger      string            `json:"logger,omitempty"`
	Caller      string            `json:"caller,omitempty"`
	Message     string            `json:"message"`
	Fields      map[string]string `json:"fields,omitempty"`
	Raw         string            `json:"raw"`
}
```

`Offset` and `NextOffset` are byte offsets in the file. They are the basis for
cursor-based paging and infinite scroll.

Line numbers are useful for display, but expensive to calculate for arbitrary
offsets in huge files. The scanner should provide exact line numbers when it
has scanned from the start or from an index checkpoint; otherwise `Line` may be
zero unless the request asks for line-number resolution.

## Parser Interface

The parser should parse one already-decoded line. Filtering and paging should
not require the parser to own file I/O; file scanning belongs to the store.

```go
type Parser interface {
	ParseLine(line []byte, meta LineMeta) (Entry, bool)
	Match(entry Entry, filter Filter) bool
}

type LineMeta struct {
	Stream     string
	Segment    string
	Line       int64
	Offset     int64
	NextOffset int64
}
```

`ParseLine` returns `false` when the line should be skipped. For most parsers,
unparseable lines should still return an `Entry` with `Raw` and `Message` set.

`Match` lets parsers implement structured filters efficiently:

- level filtering
- time range filtering
- logger/component filtering
- caller filtering
- message substring or regexp
- field predicates

The default parser can use a generic matcher after parsing.

## Query Model

```go
type Query struct {
	Cursor    Cursor
	Limit     int
	Direction Direction
	Filter    Filter
}

type Filter struct {
	Text       string
	Regex      string
	Level      []string
	Logger     []string
	Since      *time.Time
	Until      *time.Time
	Fields     map[string]string
	CaseFold   bool
}

type Direction string

const (
	Forward  Direction = "forward"
	Backward Direction = "backward"
)
```

The API should cap `Limit` server-side. A good default is 200 entries and a hard
cap around 1000.

### Cursor

Cursor should be opaque to clients. Internally it can encode:

```go
type Cursor struct {
	StreamID     string `json:"stream"`
	SegmentIndex int    `json:"segment"`
	Offset       int64  `json:"offset"`
	Line         int64  `json:"line,omitempty"`
}
```

A page response:

```go
type Page struct {
	Entries    []Entry `json:"entries"`
	NextCursor string `json:"next_cursor,omitempty"`
	PrevCursor string `json:"prev_cursor,omitempty"`
	EOF        bool   `json:"eof"`
	BOF        bool   `json:"bof"`
}
```

For infinite scroll:

- initial load calls `tail`
- scrolling up calls `page?direction=backward&cursor=...`
- scrolling down calls `page?direction=forward&cursor=...`
- filtering calls `grep` with the same cursor semantics

## Large File Strategy

The implementation must never read an entire large file into memory.

### Forward Scan

Forward paging inside one segment is straightforward:

1. open file
2. seek to cursor offset
3. read lines with a bounded `bufio.Reader`
4. parse and filter until `Limit` matching entries are found
5. return the last consumed segment/offset as `NextCursor`

When the scan reaches the end of a segment, continue into the next newer
segment until the requested limit is satisfied or the stream reaches EOF.

Long lines must be handled explicitly. Recommended policy:

- default max line bytes: 1 MiB
- if exceeded, return a truncated raw line with a `truncated` marker
- continue scanning from the true next newline

### Backward Scan

Backward paging should scan byte blocks from the end of a segment or from a
cursor offset.

Algorithm:

1. choose starting offset, usually file size for tail
2. read a block before that offset, for example 64 KiB
3. prepend any partial line carried from the previous block
4. split on `\n`
5. parse complete lines in reverse order
6. continue with earlier blocks until enough matching entries are found
7. return entries in chronological order

This avoids an up-front index and makes tailing huge files cheap.

When the scan reaches the beginning of a segment, continue into the next older
segment until the requested limit is satisfied or the stream reaches BOF.

### Optional Sparse Index

For better line numbers and faster deep jumps, add a sparse in-memory index:

```go
type Checkpoint struct {
	Offset int64
	Line   int64
}
```

Build checkpoints every N bytes or N lines while serving requests. This index is
bounded and can be discarded when file metadata changes.

The first version does not need a full index.

## Default Console Parser

The default parser should target lines shaped like:

```text
2026-07-10T10:00:06.310Z    [34mINFO[0m    boot          gateway/gateway_main.go:558    Gateway front-door request method=POST host=gateway.firegatenetworks.com path=/mcp route="/mcp" status=401 bytes=280 duration=238.122µs has_authorization=false user_agent="Claude-User"
```

Steps:

1. strip ANSI escape sequences and common escaped-color fragments such as
   `[34mINFO[0m`
2. split leading columns on runs of whitespace
3. parse timestamp with `time.RFC3339Nano`
4. parse level, logger/component, caller
5. treat the rest as message text
6. parse trailing `key=value` fields with quote handling

Parsed result:

```json
{
  "time": "2026-07-10T10:00:06.310Z",
  "level": "INFO",
  "logger": "boot",
  "caller": "gateway/gateway_main.go:558",
  "message": "Gateway front-door request",
  "fields": {
    "method": "POST",
    "host": "gateway.firegatenetworks.com",
    "path": "/mcp",
    "route": "/mcp",
    "status": "401",
    "bytes": "280",
    "duration": "238.122µs",
    "has_authorization": "false",
    "user_agent": "Claude-User"
  }
}
```

If parsing fails, the parser should still return:

```go
Entry{
	Raw:     cleanLine,
	Message: cleanLine,
}
```

## Filtering

Filtering should be applied while scanning, not after collecting an unbounded
set of entries.

Filter behavior:

- `Text` searches raw line and message
- `Regex` compiles once per request and applies to raw line and message
- `Level` matches parsed `Entry.Level`
- `Logger` matches parsed `Entry.Logger`
- `Since` / `Until` match parsed timestamps; entries without timestamps do not
  match time filters
- `Fields` matches parsed key/value fields exactly for v1

The parser interface includes `Match` so custom parsers can decide whether
their structured fields satisfy a query without forcing all parsers into the
same schema.

## HTTP API

### `GET /api/streams`

Returns logical log streams:

```json
{
  "streams": [
    {
      "id": "app.log",
      "name": "app.log",
      "segments": 3,
      "total_size": 123456,
      "newest_mod_time": "2026-07-10T10:03:00Z"
    }
  ]
}
```

### `GET /api/streams/{id}`

Returns stream metadata, including physical segments.

### `GET /api/streams/{id}/tail?limit=200`

Returns the newest retained page, ordered oldest to newest.

### `GET /api/streams/{id}/page?cursor=...&direction=backward&limit=200`

Returns an unfiltered page.

### `GET /api/streams/{id}/search?...`

Query params:

```text
q=front-door
regex=Gateway.*
level=INFO
logger=mcp
since=2026-07-10T10:00:00Z
until=2026-07-10T11:00:00Z
field.status=401
cursor=...
direction=backward
limit=200
```

### `GET /api/streams/{id}/raw?offset=0&limit=65536`

Returns a bounded raw byte range as `text/plain`. The request must identify a
segment, either through the cursor or `segment=N`. This is useful for debugging
parser behavior and for a raw mode in the UI.

## UI

The UI is a single HTML app served as the log folder preview. It should be
work-focused and dense:

- left pane: logical streams with segment count, total size, and newest mod time
- top bar: search text, regex toggle, level filter, time range
- main pane: virtualized/infinite log rows
- row fields: time, level, logger, caller, message, structured fields
- raw/parsed toggle
- copy line / copy raw controls

The browser should only hold visible pages and a small page cache. It should
not request the entire file.

The UI can be embedded with `//go:embed` similarly to existing `chiutil`
templates.

## Public API

```go
type Options struct {
	Root        string
	Parser      Parser
	Recursive   bool
	Globs       []string
	MaxLimit    int
	DefaultLimit int
	MaxLineBytes int
	BlockBytes   int
}

func New(opts Options) (*Viewer, error)

type Viewer struct {
	// private catalog, scanner, parser, options, and router
}

func (v *Viewer) ServeHTTP(w http.ResponseWriter, r *http.Request)
func (v *Viewer) Index() chiutil.FolderIndex
```

`Viewer` should satisfy `http.Handler`. It owns its internal routes, but it does
not know where it is mounted.

For integration with folder navigation, expose index metadata separately:

```go
type IndexedHandler interface {
	http.Handler
	Index() chiutil.FolderIndex
}
```

The caller remains responsible for attaching the handler to a parent router or
folder. This matches `RouteFolder`: construct a thing that owns routes, then
mount it explicitly.

Longer term, `chiutil.RouteFolder` may need a small helper for mounting an
already-built folder/router as an indexed child:

```go
func (f *RouteFolder) MountFolder(name string, child *RouteFolder)
func (f *RouteFolder) MountHandler(name, description string, handler http.Handler)
```

That helper belongs in `chiutil`, not in `logviewer`.

Defaults:

```text
Parser:        ConsoleParser
Globs:         *.log, *.txt, *.out, *
DefaultLimit:  200
MaxLimit:      1000
MaxLineBytes:  1 MiB
BlockBytes:    64 KiB
```

## Error Handling

- invalid cursor: `400`
- invalid regex: `400`
- stream not found: `404`
- root/path escape attempt: `404` or `403`
- line too long: return truncated entry when possible
- segment changed during scan: best effort; include current stream metadata in page

If a file shrinks or rotates while a cursor points past EOF, clamp the cursor to
EOF for backward/tail requests and return EOF for forward requests.

## Testing Plan

Unit tests:

- ANSI stripping
- console parser success and fallback
- key/value parsing with quoted values
- forward paging without reading full file
- backward paging from EOF
- cursor round-trip
- filtering by text, regex, level, logger, timestamp, field
- long-line truncation
- rotation/shrink behavior
- root path escape rejection

HTTP tests:

- mounted folder appears in `index.json`
- stream listing returns expected logical streams and grouped segments
- tail endpoint returns newest lines in sequence across segments
- search endpoint returns matching lines only
- preview HTML is served through the folder index

Performance tests:

- create a large temporary log file
- page through it with bounded memory
- grep for sparse matches and verify no full-file allocation

## Implementation Phases

1. Core scanner and cursor paging over one file.
2. Console parser and generic matcher.
3. File catalog rooted at a directory with path safety.
4. HTTP JSON API exposed by a viewer-owned router/folder.
5. Minimal embedded HTML viewer with file list, tail, paging, grep.
6. Optional sparse checkpoint index for faster deep paging and line numbers.

## Follow-Up Work

- Fix standalone `Viewer.ServeHTTP` navigation so it works outside a mounted
  `RouteFolder`.
- Support recursive stream IDs that contain `/` without breaking API routing or
  cursor use.
- Return surrounding context rows for backward searches, matching forward search
  behavior.
- Add an explicit plain-text parser mode for logs that do not have recognizable
  timestamp-prefixed entries.
- Cache the stream catalog for large log directories, with invalidation based on
  filesystem metadata.
- Decide whether `WriteYAML` and `WriteText` should preserve their historical
  append behavior or keep the current compatibility-breaking formatted append
  helpers.
- Treat symlink traversal as trusted-environment behavior for now; add stronger
  root confinement before using this viewer with untrusted log paths.

## Open Questions

- Should filtered `grep` search across all files, or only one selected file in
  v1?
- Should default file discovery include rotated/compressed files such as
  `.log.1` or `.gz`?
- Should the UI poll for new tail entries, or use server-sent events later?
- Should parser fields be `map[string]string` only, or allow typed values?
- Should raw ANSI be available in raw mode, or should all output use stripped
  text?
