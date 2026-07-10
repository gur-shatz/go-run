package logviewer

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gur-shatz/go-run/pkg/chiutil"
)

// Mount creates a Viewer and registers it as an ObjectsFolder under parent.
//
// The resulting backoffice structure is:
//
//	/<name>/             stream listing
//	/<name>/{stream}/    stream viewer preview
//	/<name>/{stream}/tail
//	/<name>/{stream}/page
//	/<name>/{stream}/search
//	/<name>/{stream}/raw
//	/<name>/{stream}/metadata
func Mount(parent *chiutil.RouteFolder, name string, opts Options) (*Viewer, error) {
	viewer, err := New(opts)
	if err != nil {
		return nil, err
	}
	chiutil.ObjectsFolder(parent, name, viewer).
		Title("Log files").
		Description("Browse and search generated log streams").
		ItemIndex(viewer.handleStreamHTML)
	return viewer, nil
}

// New creates a log viewer handler.
func New(opts Options) (*Viewer, error) {
	if opts.Root == "" {
		return nil, fs.ErrInvalid
	}
	info, err := fsStat(opts.Root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fs.ErrInvalid
	}
	if opts.Parser == nil {
		opts.Parser = ConsoleParser{}
	}
	if opts.DefaultLimit <= 0 {
		opts.DefaultLimit = defaultLimit
	}
	if opts.MaxLimit <= 0 {
		opts.MaxLimit = defaultMaxLimit
	}
	if opts.MaxLimit < opts.DefaultLimit {
		opts.MaxLimit = opts.DefaultLimit
	}
	if opts.MaxLineBytes <= 0 {
		opts.MaxLineBytes = defaultMaxLineBytes
	}
	if opts.BlockBytes <= 0 {
		opts.BlockBytes = defaultBlockBytes
	}
	if len(opts.Globs) == 0 {
		opts.Globs = []string{"*.log", "*.txt", "*.out", "*"}
	}
	v := &Viewer{opts: opts, parser: opts.Parser}
	r := chi.NewRouter()
	r.Get("/", v.handleHTML)
	r.Get("/index.json", v.handleIndex)
	r.Get("/api/streams", v.handleStreams)
	r.Get("/api/streams/{id}", v.handleStream)
	r.Get("/api/streams/{id}/tail", v.handleTail)
	r.Get("/api/streams/{id}/page", v.handlePage)
	r.Get("/api/streams/{id}/search", v.handlePage)
	r.Get("/api/streams/{id}/raw", v.handleRaw)
	v.router = r
	return v, nil
}

// ListItems implements chiutil.ObjectMapper for ObjectsFolder integration.
func (v *Viewer) ListItems() []chiutil.ObjectEntry {
	streams, err := v.streams()
	if err != nil {
		return nil
	}
	summaries := summarize(streams)
	entries := make([]chiutil.ObjectEntry, 0, len(summaries))
	for _, summary := range summaries {
		entries = append(entries, chiutil.ObjectEntry{
			ID:          summary.ID,
			Name:        summary.Name,
			Description: formatStreamDescription(summary),
		})
	}
	return entries
}

// GetItem implements chiutil.ObjectMapper for ObjectsFolder integration.
func (v *Viewer) GetItem(id string) (Stream, bool) {
	stream, err := v.stream(id)
	return stream, err == nil
}

// Routes implements chiutil.ObjectMapper for ObjectsFolder integration.
func (v *Viewer) Routes() []chiutil.ObjectRoute[Stream] {
	return []chiutil.ObjectRoute[Stream]{
		{Method: http.MethodGet, Path: "/metadata", Handler: v.handleObjectMetadata, Description: "Stream metadata"},
		{Method: http.MethodGet, Path: "/tail", Handler: v.handleObjectTail, Description: "Newest log page"},
		{Method: http.MethodGet, Path: "/page", Handler: v.handleObjectPage, Description: "Cursor-based log page"},
		{Method: http.MethodGet, Path: "/search", Handler: v.handleObjectPage, Description: "Server-side filtered log page"},
		{Method: http.MethodGet, Path: "/raw", Handler: v.handleObjectRaw, Description: "Bounded raw byte range"},
	}
}

// ServeHTTP implements http.Handler.
func (v *Viewer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.router.ServeHTTP(w, r)
}

// Index returns folder metadata for callers that want to expose it separately.
func (v *Viewer) Index() chiutil.FolderIndex {
	return chiutil.FolderIndex{
		Title:       "Log files",
		Description: "Browse and search log streams",
		Path:        "/",
		HasIndex:    true,
		Entries: []*chiutil.RouteEntry{
			{Name: "api/streams", Method: "GET", Path: "api/streams", Description: "List logical streams"},
		},
	}
}

func (v *Viewer) handleHTML(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(logviewerHTML)
}

func (v *Viewer) handleStreamHTML(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(logstreamHTML)
}

func (v *Viewer) handleIndex(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, v.Index())
}

func (v *Viewer) handleStreams(w http.ResponseWriter, _ *http.Request) {
	streams, err := v.streams()
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, map[string]any{"streams": summarize(streams)})
}

func (v *Viewer) handleObjectMetadata(stream Stream, w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, stream)
}

func (v *Viewer) handleStream(w http.ResponseWriter, r *http.Request) {
	stream, err := v.stream(chi.URLParam(r, "id"))
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, stream)
}

func (v *Viewer) handleObjectTail(stream Stream, w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		httpError(w, errInvalidFilter)
		return
	}
	page, err := v.tailWithFilter(stream, parseLimit(r, v.opts.DefaultLimit), filter)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, page)
}

func (v *Viewer) handleTail(w http.ResponseWriter, r *http.Request) {
	stream, err := v.stream(chi.URLParam(r, "id"))
	if err != nil {
		httpError(w, err)
		return
	}
	filter, err := parseFilter(r)
	if err != nil {
		httpError(w, errInvalidFilter)
		return
	}
	page, err := v.tailWithFilter(stream, parseLimit(r, v.opts.DefaultLimit), filter)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, page)
}

func (v *Viewer) handleObjectPage(stream Stream, w http.ResponseWriter, r *http.Request) {
	page, err := v.pageFromRequest(stream, r)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, page)
}

func (v *Viewer) handlePage(w http.ResponseWriter, r *http.Request) {
	stream, err := v.stream(chi.URLParam(r, "id"))
	if err != nil {
		httpError(w, err)
		return
	}
	page, err := v.pageFromRequest(stream, r)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, page)
}

func (v *Viewer) pageFromRequest(stream Stream, r *http.Request) (Page, error) {
	cursor, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		return Page{}, err
	}
	if cursor.StreamID == "" {
		cursor = Cursor{StreamID: stream.ID, SegmentIndex: 0, Offset: 0}
	}
	direction := Direction(r.URL.Query().Get("direction"))
	if direction == "" {
		direction = Forward
	}
	if direction != Forward && direction != Backward {
		return Page{}, errInvalidDirection
	}
	filter, err := parseFilter(r)
	if err != nil {
		return Page{}, errInvalidFilter
	}
	return v.scan(stream, Query{
		Cursor:    cursor,
		Limit:     parseLimit(r, v.opts.DefaultLimit),
		Direction: direction,
		Filter:    filter,
	})
}

func (v *Viewer) handleObjectRaw(stream Stream, w http.ResponseWriter, r *http.Request) {
	if err := v.serveRaw(stream, w, r); err != nil {
		httpError(w, err)
	}
}

func (v *Viewer) handleRaw(w http.ResponseWriter, r *http.Request) {
	stream, err := v.stream(chi.URLParam(r, "id"))
	if err != nil {
		httpError(w, err)
		return
	}
	if err := v.serveRaw(stream, w, r); err != nil {
		httpError(w, err)
	}
}

func (v *Viewer) serveRaw(stream Stream, w http.ResponseWriter, r *http.Request) error {
	segment := 0
	var err error
	if rawSegment := r.URL.Query().Get("segment"); rawSegment != "" {
		segment, err = strconv.Atoi(rawSegment)
		if err != nil {
			return errInvalidSegment
		}
	} else if cursorValue := r.URL.Query().Get("cursor"); cursorValue != "" {
		cursor, err := decodeCursor(cursorValue)
		if err != nil {
			return err
		}
		segment = cursor.SegmentIndex
	}
	if segment < 0 || segment >= len(stream.Segments) {
		return errInvalidSegment
	}
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	limit := int64(65536)
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		limit, err = strconv.ParseInt(rawLimit, 10, 64)
		if err != nil || limit < 0 {
			return errInvalidLimit
		}
	}
	if limit > 1<<20 {
		limit = 1 << 20
	}
	full, err := v.segmentPath(stream.Segments[segment])
	if err != nil {
		return err
	}
	f, err := fsOpen(full)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.CopyN(w, f, limit)
	return nil
}

func parseLimit(r *http.Request, fallback int) int {
	limit := fallback
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	return limit
}

func parseFilter(r *http.Request) (Filter, error) {
	q := r.URL.Query()
	filter := Filter{
		Text:     q.Get("q"),
		Regex:    q.Get("regex"),
		Level:    q["level"],
		Logger:   q["logger"],
		Fields:   map[string]string{},
		CaseFold: q.Get("case_fold") == "true" || q.Get("casefold") == "true",
	}
	contextSet := false
	if raw := q.Get("context"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return Filter{}, errInvalidFilter
		}
		filter.Before = min(n, 20)
		filter.After = min(n, 20)
		contextSet = true
	}
	if raw := q.Get("context_before"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return Filter{}, errInvalidFilter
		}
		filter.Before = min(n, 20)
		contextSet = true
	}
	if raw := q.Get("context_after"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return Filter{}, errInvalidFilter
		}
		filter.After = min(n, 20)
		contextSet = true
	}
	for key, values := range q {
		if !strings.HasPrefix(key, "field.") || len(values) == 0 {
			continue
		}
		filter.Fields[strings.TrimPrefix(key, "field.")] = values[0]
	}
	if len(filter.Fields) == 0 {
		filter.Fields = nil
	}
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Filter{}, err
		}
		filter.Since = &t
	}
	if raw := q.Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return Filter{}, err
		}
		filter.Until = &t
	}
	if !contextSet && (filter.Text != "" || filter.Regex != "") {
		filter.Before = 3
		filter.After = 3
	}
	return prepareFilter(filter)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func httpError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errStreamNotFound):
		http.Error(w, "stream not found", http.StatusNotFound)
	case errors.Is(err, errInvalidCursor), errors.Is(err, fs.ErrInvalid), errors.Is(err, errInvalidDirection), errors.Is(err, errInvalidSegment), errors.Is(err, errInvalidLimit), errors.Is(err, errInvalidFilter):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, fs.ErrPermission):
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func formatStreamDescription(summary streamSummary) string {
	return strconv.Itoa(summary.Segments) + " segments, " + strconv.FormatInt(summary.TotalSize, 10) + " bytes"
}
