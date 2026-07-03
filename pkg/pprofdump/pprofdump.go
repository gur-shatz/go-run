// Package pprofdump provides an HTTP handler that writes a bounded set of Go
// pprof dumps to disk on demand. It is intended for "last chance" diagnostics:
// register the handler in an application, then have an external supervisor call
// it before terminating the process for memory pressure.
package pprofdump

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Profile describes one runtime/pprof profile to write.
type Profile struct {
	// Name is the pprof profile name, e.g. "heap" or "goroutine".
	Name string
	// Debug is passed to Profile.WriteTo. 0 writes the binary profile format;
	// values >0 write human-readable text for profiles that support it.
	Debug int
	// File overrides the output filename. When empty, a stable name is derived
	// from Name and Debug.
	File string
}

// Options configures Handler.
type Options struct {
	// Dir is the parent directory where one timestamped dump directory is
	// created per request. Required.
	Dir string
	// Profiles is the list to write. Empty uses DefaultProfiles.
	Profiles []Profile
	// RuntimeGC runs runtime.GC before writing heap-like profiles. Default true.
	RuntimeGC *bool
	// Now overrides the clock in tests.
	Now func() time.Time
}

// DumpResponse is the JSON response returned by the handler.
type DumpResponse struct {
	Dir   string   `json:"dir"`
	Files []string `json:"files"`
	Error string   `json:"error,omitempty"`
}

// DefaultProfiles returns a useful, low-surprise diagnostic set for memory
// pressure. Heap and allocs are binary pprof profiles; goroutines are text so a
// human can inspect blocked handlers without tooling.
func DefaultProfiles() []Profile {
	return []Profile{
		{Name: "heap", Debug: 0, File: "heap.pprof"},
		{Name: "allocs", Debug: 0, File: "allocs.pprof"},
		{Name: "goroutine", Debug: 2, File: "goroutine.txt"},
		{Name: "threadcreate", Debug: 0, File: "threadcreate.pprof"},
		{Name: "block", Debug: 0, File: "block.pprof"},
		{Name: "mutex", Debug: 0, File: "mutex.pprof"},
	}
}

// Handler returns an HTTP handler that accepts POST and writes pprof dumps.
func Handler(opts Options) http.Handler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if len(opts.Profiles) == 0 {
		opts.Profiles = DefaultProfiles()
	}
	runGC := true
	if opts.RuntimeGC != nil {
		runGC = *opts.RuntimeGC
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.TrimSpace(opts.Dir) == "" {
			writeError(w, http.StatusInternalServerError, "pprofdump: Dir is required")
			return
		}
		if runGC {
			runtime.GC()
		}
		dir, files, err := writeDump(opts.Dir, opts.Profiles, opts.Now())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, DumpResponse{Dir: dir, Files: files})
	})
}

// Register mounts Handler on a standard ServeMux. Routers that accept
// http.Handler can use Handler directly.
func Register(mux *http.ServeMux, path string, opts Options) {
	mux.Handle(path, Handler(opts))
}

// RegisterChi mounts Handler on a chi router using POST. Use Handler directly
// when a different router abstraction accepts http.Handler values.
func RegisterChi(r chi.Router, path string, opts Options) {
	r.Method(http.MethodPost, path, Handler(opts))
}

func writeDump(parent string, profiles []Profile, now time.Time) (string, []string, error) {
	dir := filepath.Join(parent, "pprof-"+now.UTC().Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create dump dir: %w", err)
	}
	files := make([]string, 0, len(profiles))
	for _, spec := range profiles {
		prof := pprof.Lookup(spec.Name)
		if prof == nil {
			continue
		}
		name := spec.File
		if name == "" {
			name = profileFileName(spec)
		}
		if filepath.Base(name) != name {
			return "", nil, fmt.Errorf("profile %s has unsafe file name %q", spec.Name, name)
		}
		path := filepath.Join(dir, name)
		if err := writeOne(path, prof, spec.Debug); err != nil {
			return "", nil, fmt.Errorf("write %s: %w", spec.Name, err)
		}
		files = append(files, name)
	}
	return dir, files, nil
}

func writeOne(path string, prof *pprof.Profile, debug int) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return prof.WriteTo(f, debug)
}

func profileFileName(spec Profile) string {
	if spec.Debug > 0 {
		return spec.Name + ".txt"
	}
	return spec.Name + ".pprof"
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, DumpResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v DumpResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
