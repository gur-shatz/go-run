package logviewer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/gur-shatz/go-run/pkg/chiutil"
)

func TestConsoleParser(t *testing.T) {
	line := []byte(`2026-07-10T10:00:06.310Z    [34mINFO[0m    boot          gateway/gateway_main.go:558    Gateway front-door request method=POST host=gateway.firegatenetworks.com route="/mcp" status=401 user_agent="Claude-User"`)
	entry, ok := (ConsoleParser{}).ParseLine(line, LineMeta{Stream: "gateway.log", Segment: "gateway.log", Offset: 5, NextOffset: 10})
	if !ok {
		t.Fatal("expected parser to keep line")
	}
	if entry.Level != "INFO" {
		t.Fatalf("level = %q, want INFO", entry.Level)
	}
	if entry.Logger != "boot" {
		t.Fatalf("logger = %q, want boot", entry.Logger)
	}
	if entry.Message != "Gateway front-door request" {
		t.Fatalf("message = %q", entry.Message)
	}
	if entry.Fields["route"] != "/mcp" || entry.Fields["status"] != "401" || entry.Fields["user_agent"] != "Claude-User" {
		t.Fatalf("fields = %#v", entry.Fields)
	}
	if strings.Contains(entry.Raw, "[34m") || strings.Contains(entry.Raw, "[0m") {
		t.Fatalf("raw still contains color markers: %q", entry.Raw)
	}
}

func TestConsoleParserAcceptsCompactTimezoneOffset(t *testing.T) {
	line := []byte("2026-07-06T23:51:02.360+0300\t\x1b[35mDEBUG\x1b[0m\tboot\tserver/backend_main.go:187\tGET /health status=200")
	parser := ConsoleParser{}
	if !parser.StartsEntryLine(line) {
		t.Fatal("compact timezone offset was not recognized as an entry")
	}
	entry, ok := parser.ParseLine(line, LineMeta{})
	if !ok || entry.Time == nil {
		t.Fatalf("entry was not parsed: %#v", entry)
	}
	_, offset := entry.Time.Zone()
	if offset != 3*60*60 {
		t.Fatalf("timezone offset = %d, want %d", offset, 3*60*60)
	}
	if entry.Level != "DEBUG" || entry.Message != "GET /health" || entry.Fields["status"] != "200" {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestPrepareFilterReusesCompiledRegex(t *testing.T) {
	filter, err := prepareFilter(Filter{Regex: `status=(?:200|500)`})
	if err != nil {
		t.Fatal(err)
	}
	if filter.regex == nil {
		t.Fatal("regex was not compiled")
	}
	compiled := filter.regex
	filter, err = prepareFilter(filter)
	if err != nil {
		t.Fatal(err)
	}
	if filter.regex != compiled {
		t.Fatal("prepared regex was compiled again")
	}
	if !matchEntry(Entry{Raw: "request status=500"}, filter) {
		t.Fatal("prepared regex did not match")
	}
}

func TestInvalidRegexReturnsBadRequest(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "app.log", "2026-07-10T10:00:00Z INFO boot app.go:1 ready\n")
	viewer := newTestViewer(t, root)
	req := httptest.NewRequest(http.MethodGet, "/api/streams/app.log/search?regex=%5B", nil)
	rr := httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
}

func TestStreamsGroupRotatedSegments(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "gateway.log.2", "old\n")
	writeFile(t, root, "gateway.log.1", "middle\n")
	writeFile(t, root, "gateway.log", "new\n")

	viewer := newTestViewer(t, root)
	streams, err := viewer.streams()
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 1 {
		t.Fatalf("streams = %d, want 1: %#v", len(streams), streams)
	}
	got := []string{streams[0].Segments[0].Path, streams[0].Segments[1].Path, streams[0].Segments[2].Path}
	want := []string{"gateway.log.2", "gateway.log.1", "gateway.log"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("segments = %#v, want %#v", got, want)
		}
	}
}

func TestStreamsFilterByPrefixes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "gateway.log.1", "old\n")
	writeFile(t, root, "gateway.log", "new\n")
	writeFile(t, root, "worker.log", "other\n")
	writeFile(t, root, "scheduler.log", "excluded\n")

	viewer, err := New(Options{Root: root, Prefixes: []string{"gateway", "worker"}})
	if err != nil {
		t.Fatal(err)
	}
	streams, err := viewer.streams()
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 2 || streams[0].Name != "gateway.log" || streams[1].Name != "worker.log" {
		t.Fatalf("streams = %#v, want gateway.log and worker.log", streams)
	}
	if len(streams[0].Segments) != 2 {
		t.Fatalf("segments = %#v, want both gateway rotations", streams[0].Segments)
	}
	if _, ok := viewer.GetItem("scheduler.log"); ok {
		t.Fatal("non-matching stream should not be addressable")
	}
}

func TestTailPageAndSearchHTTP(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "app.log.1", strings.Join([]string{
		"2026-07-10T10:00:00Z INFO boot app.go:1 old-one status=200",
		"2026-07-10T10:01:00Z WARN boot app.go:2 old-two status=401",
		"",
	}, "\n"))
	writeFile(t, root, "app.log", strings.Join([]string{
		"2026-07-10T10:02:00Z INFO api app.go:3 new-one status=200",
		"2026-07-10T10:03:00Z ERROR api app.go:4 new-two status=500",
		"",
	}, "\n"))

	viewer := newTestViewer(t, root)
	req := httptest.NewRequest(http.MethodGet, "/api/streams/app.log/tail?limit=2", nil)
	rr := httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("tail status = %d body=%s", rr.Code, rr.Body.String())
	}
	var page Page
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("tail entries = %d: %#v", len(page.Entries), page.Entries)
	}
	if page.Range.EntryCount != 2 || page.Range.TotalBytes == 0 || page.Range.StartAbsolute >= page.Range.EndAbsolute {
		t.Fatalf("tail range = %#v", page.Range)
	}
	if page.Entries[0].Message != "new-one" || page.Entries[1].Message != "new-two" {
		t.Fatalf("tail messages = %#v", []string{page.Entries[0].Message, page.Entries[1].Message})
	}
	tailPage := page

	req = httptest.NewRequest(http.MethodGet, "/api/streams/app.log/tail?limit=2&q=new-two", nil)
	rr = httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("filtered tail status = %d body=%s", rr.Code, rr.Body.String())
	}
	page = Page{}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Message != "new-two" {
		t.Fatalf("filtered tail entries = %#v", page.Entries)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/streams/app.log/page?direction=backward&limit=2&cursor="+tailPage.PrevCursor, nil)
	rr = httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("older status = %d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 || page.Entries[0].Message != "old-one" || page.Entries[1].Message != "old-two" {
		t.Fatalf("older page = %#v", page.Entries)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/streams/app.log/page?direction=backward&limit=2&cursor="+page.PrevCursor, nil)
	rr = httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("past-start status = %d body=%s", rr.Code, rr.Body.String())
	}
	page = Page{}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 0 || !page.BOF {
		t.Fatalf("past-start page = %#v bof=%v", page.Entries, page.BOF)
	}
	if page.PrevCursor != "" {
		t.Fatalf("past-start prev cursor = %q, want empty", page.PrevCursor)
	}
	if !strings.Contains(rr.Body.String(), `"entries":[]`) {
		t.Fatalf("past-start entries should encode as []: %s", rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/streams/app.log/search?direction=forward&field.status=401", nil)
	rr = httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Message != "old-two" {
		t.Fatalf("search page = %#v", page.Entries)
	}
}

func TestRawEndpoint(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "app.log", "abcdef\n")
	viewer := newTestViewer(t, root)

	req := httptest.NewRequest(http.MethodGet, "/api/streams/app.log/raw?segment=0&offset=1&limit=3", nil)
	rr := httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("raw status = %d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "bcd" {
		t.Fatalf("raw = %q, want bcd", rr.Body.String())
	}
}

func TestSearchContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "app.log", strings.Join([]string{
		"2026-07-10T10:00:00Z INFO boot app.go:1 one status=200",
		"2026-07-10T10:00:01Z INFO boot app.go:2 two match=yes",
		"2026-07-10T10:00:02Z INFO boot app.go:3 three status=200",
		"2026-07-10T10:00:03Z INFO boot app.go:4 four match=yes",
		"2026-07-10T10:00:04Z INFO boot app.go:5 five status=200",
		"",
	}, "\n"))
	viewer := newTestViewer(t, root)

	req := httptest.NewRequest(http.MethodGet, "/api/streams/app.log/search?q=match&context=1&limit=10", nil)
	rr := httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rr.Code, rr.Body.String())
	}
	var page Page
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(page.Entries))
	matches := 0
	for _, entry := range page.Entries {
		got = append(got, entry.Message)
		if entry.Match {
			matches++
		}
	}
	want := []string{"one", "two", "three", "four", "five"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
	if matches != 2 {
		t.Fatalf("matches = %d, want 2: %#v", matches, page.Entries)
	}
	if !page.Entries[0].ContextTop {
		t.Fatalf("first context row should have top edge: %#v", page.Entries[0])
	}
	if !page.Entries[len(page.Entries)-1].ContextBot {
		t.Fatalf("last context row should have bottom edge: %#v", page.Entries[len(page.Entries)-1])
	}
	if page.Entries[2].ContextTop || page.Entries[2].ContextBot {
		t.Fatalf("merged middle context should not be an edge: %#v", page.Entries[2])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/streams/app.log/search?q=match&limit=10", nil)
	rr = httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("default context search status = %d body=%s", rr.Code, rr.Body.String())
	}
	page = Page{}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 5 {
		t.Fatalf("default context entries = %d, want 5: %#v", len(page.Entries), page.Entries)
	}
}

func TestMultilineConsoleEntries(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "app.log", strings.Join([]string{
		"2026-07-10T10:00:00Z ERROR boot app.go:1 failed request status=500",
		"stack frame one",
		"stack frame two",
		"==== separator ====",
		"2026-07-10T10:00:01Z INFO boot app.go:2 recovered status=200",
		"",
	}, "\n"))
	viewer := newTestViewer(t, root)

	req := httptest.NewRequest(http.MethodGet, "/api/streams/app.log/tail?limit=2", nil)
	rr := httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("tail status = %d body=%s", rr.Code, rr.Body.String())
	}
	var page Page
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("tail entries = %#v", page.Entries)
	}
	if !strings.Contains(page.Entries[0].Raw, "stack frame one") || !strings.Contains(page.Entries[0].Raw, "stack frame two") {
		t.Fatalf("continuation lines missing from raw: %q", page.Entries[0].Raw)
	}
	if strings.Contains(page.Entries[0].Raw, "====") {
		t.Fatalf("separator should be ignored: %q", page.Entries[0].Raw)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/streams/app.log/search?q=stack%20frame%20two&context=0", nil)
	rr = httptest.NewRecorder()
	viewer.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rr.Code, rr.Body.String())
	}
	page = Page{}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Message != "failed request" {
		t.Fatalf("search entries = %#v", page.Entries)
	}
}

func TestCompactTimezoneMultilineTail(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "backend.log", strings.Join([]string{
		"2026-07-06T23:51:02.360+0300 DEBUG boot server/backend_main.go:187 request payload status=200",
		`  "authenticated": true,`,
		`  "authentication_source": 1`,
		"2026-07-06T23:51:03.361+0300 INFO boot server/backend_main.go:188 request complete status=200",
		"",
	}, "\n"))
	viewer := newTestViewer(t, root)
	stream, err := viewer.stream("backend.log")
	if err != nil {
		t.Fatal(err)
	}
	page, err := viewer.tail(stream, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("entries = %#v", page.Entries)
	}
	if !strings.Contains(page.Entries[0].Raw, `"authenticated": true`) {
		t.Fatalf("multiline payload missing: %q", page.Entries[0].Raw)
	}
	if page.Entries[1].Message != "request complete" {
		t.Fatalf("last entry = %#v", page.Entries[1])
	}
}

func TestReverseLineBufferRetainsBoundedPrefix(t *testing.T) {
	const (
		maxBytes  = 1024
		chunkSize = 97
	)
	line := make([]byte, 8*maxBytes)
	for i := range line {
		line[i] = byte('a' + i%26)
	}

	var buffer reverseLineBuffer
	for end := len(line); end > 0; {
		start := max(0, end-chunkSize)
		buffer.prepend(line[start:end], maxBytes)
		if buffer.len() > maxBytes || len(buffer.buf) > maxBytes {
			t.Fatalf("buffer grew beyond cap: len=%d capacity=%d", buffer.len(), len(buffer.buf))
		}
		end = start
	}

	if !buffer.truncated {
		t.Fatal("oversized line was not marked truncated")
	}
	if got := buffer.bytes(); !bytes.Equal(got, line[:maxBytes]) {
		t.Fatalf("retained bytes are not the line prefix: got=%q want=%q", got[:32], line[:32])
	}
}

func TestTailBoundsOversizedPhysicalLine(t *testing.T) {
	const (
		fileSize     = 8 << 20
		maxLineBytes = 4 << 10
		blockBytes   = 1024
	)
	root := t.TempDir()
	path := filepath.Join(root, "huge.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "2026-07-10T10:00:00Z INFO boot app.go:1 oversized-payload-"
	if _, err := f.WriteString(prefix); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Truncate(fileSize); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	viewer, err := New(Options{
		Root:         root,
		BlockBytes:   blockBytes,
		MaxLineBytes: maxLineBytes,
		DefaultLimit: 2,
		MaxLimit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := viewer.stream("huge.log")
	if err != nil {
		t.Fatal(err)
	}
	page, err := viewer.tail(stream, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(page.Entries))
	}
	entry := page.Entries[0]
	if !entry.Truncated {
		t.Fatal("oversized entry was not marked truncated")
	}
	if len(entry.Raw) > maxLineBytes {
		t.Fatalf("raw length = %d, want <= %d", len(entry.Raw), maxLineBytes)
	}
	if !strings.HasPrefix(entry.Raw, prefix) {
		t.Fatalf("raw no longer starts with structured prefix: %q", entry.Raw[:min(len(entry.Raw), len(prefix))])
	}
	if entry.Offset != 0 || entry.NextOffset != fileSize {
		t.Fatalf("entry offsets = %d..%d, want 0..%d", entry.Offset, entry.NextOffset, fileSize)
	}
}

func BenchmarkTailOversizedPhysicalLine(b *testing.B) {
	const (
		fileSize     = 16 << 20
		maxLineBytes = 64 << 10
		blockBytes   = 64 << 10
	)
	root := b.TempDir()
	path := filepath.Join(root, "huge.log")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := f.WriteString("2026-07-10T10:00:00Z INFO boot app.go:1 oversized-payload-"); err != nil {
		f.Close()
		b.Fatal(err)
	}
	if err := f.Truncate(fileSize); err != nil {
		f.Close()
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}

	viewer, err := New(Options{
		Root:         root,
		BlockBytes:   blockBytes,
		MaxLineBytes: maxLineBytes,
		DefaultLimit: 1,
		MaxLimit:     1,
	})
	if err != nil {
		b.Fatal(err)
	}
	stream, err := viewer.stream("huge.log")
	if err != nil {
		b.Fatal(err)
	}

	b.SetBytes(fileSize)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		page, err := viewer.tail(stream, 1)
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Entries) != 1 || !page.Entries[0].Truncated {
			b.Fatalf("unexpected page: %#v", page)
		}
	}
}

func BenchmarkTailUnrecognizedLines(b *testing.B) {
	const fileSize = 8 << 20
	root := b.TempDir()
	line := "plain line without a recognized timestamp\n"
	content := strings.Repeat(line, fileSize/len(line))
	writeFile(b, root, "plain.log", content)

	viewer, err := New(Options{
		Root:         root,
		BlockBytes:   64 << 10,
		MaxLineBytes: 64 << 10,
		DefaultLimit: 1,
		MaxLimit:     1,
	})
	if err != nil {
		b.Fatal(err)
	}
	stream, err := viewer.stream("plain.log")
	if err != nil {
		b.Fatal(err)
	}

	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		page, err := viewer.tail(stream, 1)
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Entries) != 0 {
			b.Fatalf("unexpected entries: %#v", page.Entries)
		}
	}
}

func TestObjectsFolderMount(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "app.log.1", "2026-07-10T10:00:00Z INFO boot app.go:1 old status=200\n")
	writeFile(t, root, "app.log", "2026-07-10T10:01:00Z INFO boot app.go:2 new status=200\n")

	router := chi.NewRouter()
	folder := chiutil.NewRouteFolder(router, "/backoffice")
	if _, err := Mount(folder, "logs", Options{Root: root}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/backoffice/logs/index.json", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var index chiutil.FolderIndex
	if err := json.Unmarshal(rr.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != 1 || index.Entries[0].Name != "app.log" || !index.Entries[0].IsFolder {
		t.Fatalf("list entries = %#v", index.Entries)
	}

	req = httptest.NewRequest(http.MethodGet, "/backoffice/logs/app.log/index.json", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("item status = %d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if !hasEntry(index.Entries, "tail") || !hasEntry(index.Entries, "search") || !hasEntry(index.Entries, "raw") {
		t.Fatalf("item entries = %#v", index.Entries)
	}

	req = httptest.NewRequest(http.MethodGet, "/backoffice/logs/app.log/tail?limit=1", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("tail status = %d body=%s", rr.Code, rr.Body.String())
	}
	var page Page
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Message != "new" {
		t.Fatalf("tail entries = %#v", page.Entries)
	}
	if page.Range.StreamID != "app.log" || page.Range.EntryCount != 1 || page.Range.TotalBytes == 0 {
		t.Fatalf("range = %#v", page.Range)
	}

	req = httptest.NewRequest(http.MethodGet, "/backoffice/logs/app.log/?preview=true", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview status = %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `class="side"`) || strings.Contains(rr.Body.String(), `id="streams"`) {
		t.Fatalf("stream preview still includes standalone sidebar")
	}
}

func newTestViewer(t *testing.T, root string) *Viewer {
	t.Helper()
	viewer, err := New(Options{Root: root, BlockBytes: 16, DefaultLimit: 2, MaxLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return viewer
}

func writeFile(t testing.TB, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasEntry(entries []*chiutil.RouteEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
}
