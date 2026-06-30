package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gur-shatz/go-run/internal/log"
)

// memoryDir is the rolling time-series location under the supervisor state
// directory. It reuses state_dir rather than a separate path.
func memoryDir(paths Paths) string { return filepath.Join(paths.StateDir, "memory") }

// memoryPersister writes the rolling memory series to disk. The hot path stays
// cheap: current.json is replaced atomically each sample, and historical
// samples are appended as compact NDJSON to a per-day file. Old day files are
// pruned past the retention window. Every error is logged and swallowed —
// persistence must never disrupt sampling or supervision.
type memoryPersister struct {
	dir       string
	retention time.Duration
	logger    *log.Logger

	mu     sync.Mutex
	day    string   // YYYY-MM-DD of the currently open append file
	append *os.File // append handle for today's NDJSON, lazily (re)opened
}

func newMemoryPersister(dir string, retention time.Duration, logger *log.Logger) *memoryPersister {
	return &memoryPersister{dir: dir, retention: retention, logger: logger}
}

// write replaces current.json and appends one NDJSON line for the sample. now
// dates the per-day file and triggers a prune when the day rolls.
func (this *memoryPersister) write(sample memorySample, now time.Time) {
	if this == nil {
		return
	}
	line, err := json.Marshal(sample)
	if err != nil {
		this.warn("marshal memory sample: %v", err)
		return
	}

	if err := atomicWrite(filepath.Join(this.dir, "current.json"), append(line, '\n'), 0644); err != nil {
		this.warn("write current.json: %v", err)
	}

	this.mu.Lock()
	defer this.mu.Unlock()

	day := now.UTC().Format("2006-01-02")
	if this.append == nil || this.day != day {
		this.rotateLocked(day, now)
	}
	if this.append != nil {
		if _, err := this.append.Write(append(line, '\n')); err != nil {
			this.warn("append memory sample: %v", err)
		}
	}
}

// rotateLocked closes the previous day's file, opens today's, and prunes
// expired files. Caller holds mu.
func (this *memoryPersister) rotateLocked(day string, now time.Time) {
	if this.append != nil {
		_ = this.append.Close()
		this.append = nil
	}
	if err := os.MkdirAll(this.dir, 0755); err != nil {
		this.warn("mkdir %s: %v", this.dir, err)
		return
	}
	path := filepath.Join(this.dir, "samples-"+day+".ndjson")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		this.warn("open %s: %v", path, err)
		return
	}
	this.append = f
	this.day = day
	this.pruneLocked(now)
}

// pruneExpired removes day files older than the retention window. Safe to call
// at startup before any append handle exists.
func (this *memoryPersister) pruneExpired(now time.Time) {
	if this == nil {
		return
	}
	this.mu.Lock()
	defer this.mu.Unlock()
	this.pruneLocked(now)
}

func (this *memoryPersister) pruneLocked(now time.Time) {
	if this.retention <= 0 {
		return
	}
	entries, err := os.ReadDir(this.dir)
	if err != nil {
		return // dir not created yet — nothing to prune
	}
	cutoff := now.UTC().Add(-this.retention)
	for _, e := range entries {
		name := e.Name()
		datePart, ok := strings.CutPrefix(name, "samples-")
		if !ok || !strings.HasSuffix(datePart, ".ndjson") {
			continue
		}
		datePart = strings.TrimSuffix(datePart, ".ndjson")
		day, err := time.Parse("2006-01-02", datePart)
		if err != nil {
			continue
		}
		// Compare against the end of that day so a same-day boundary is kept.
		if day.AddDate(0, 0, 1).Before(cutoff) {
			_ = os.Remove(filepath.Join(this.dir, name))
		}
	}
}

// incidentMeta is the header view of an incident file for the listing endpoint.
type incidentMeta struct {
	File      string     `json:"file" yaml:"file"`
	TS        string     `json:"ts" yaml:"ts"`
	Kind      string     `json:"kind" yaml:"kind"`
	Component string     `json:"component,omitempty" yaml:"component,omitempty"`
	Reason    string     `json:"reason,omitempty" yaml:"reason,omitempty"`
	Mode      MemoryMode `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// writeIncident persists one incident under incidents/<ts>-<kind>.json. Errors
// are logged and swallowed.
func (this *memoryPersister) writeIncident(incident memoryIncident, ts time.Time) {
	if this == nil {
		return
	}
	dir := filepath.Join(this.dir, "incidents")
	if err := os.MkdirAll(dir, 0755); err != nil {
		this.warn("mkdir %s: %v", dir, err)
		return
	}
	data, err := json.Marshal(incident)
	if err != nil {
		this.warn("marshal incident: %v", err)
		return
	}
	path := filepath.Join(dir, incidentFilename(ts, incident.Kind))
	if err := atomicWrite(path, append(data, '\n'), 0644); err != nil {
		this.warn("write incident %s: %v", path, err)
	}
}

// listIncidents reads incident headers (without the sample payload), newest
// first. Returns nil when the directory does not exist yet.
func (this *memoryPersister) listIncidents() []incidentMeta {
	if this == nil {
		return nil
	}
	dir := filepath.Join(this.dir, "incidents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]incidentMeta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		// Decode into a header struct; the heavy "samples" field is ignored.
		var hdr struct {
			TS        string     `json:"ts"`
			Kind      string     `json:"kind"`
			Component string     `json:"component"`
			Reason    string     `json:"reason"`
			Mode      MemoryMode `json:"mode"`
		}
		if err := json.Unmarshal(raw, &hdr); err != nil {
			continue
		}
		out = append(out, incidentMeta{
			File: e.Name(), TS: hdr.TS, Kind: hdr.Kind,
			Component: hdr.Component, Reason: hdr.Reason, Mode: hdr.Mode,
		})
	}
	// Newest first by timestamp (filenames sort chronologically too, but ts is
	// authoritative).
	sort.Slice(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	return out
}

func (this *memoryPersister) warn(format string, args ...any) {
	if this.logger != nil {
		this.logger.Warn("memory: "+format, args...)
	}
}
