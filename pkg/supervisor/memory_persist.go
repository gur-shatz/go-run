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

// memoryPersister writes the rolling memory series to disk in two tiers. The
// hot path stays cheap: current.json is replaced atomically each sample, raw
// samples are appended as compact NDJSON to a per-day file, and each completed
// wall-clock minute is folded into a 1-minute rollup line. Raw files are pruned
// at raw_window (the live view and incidents read raw); rollup files are kept to
// retention (the sparkline and longer-range queries read rollups). Every error
// is logged and swallowed — persistence must never disrupt supervision.
type memoryPersister struct {
	dir       string
	rawWindow time.Duration
	retention time.Duration
	logger    *log.Logger

	mu     sync.Mutex
	day    string   // YYYY-MM-DD of the currently open raw append file
	append *os.File // append handle for today's raw NDJSON, lazily (re)opened
	acc    *minuteAccumulator
}

func newMemoryPersister(dir string, rawWindow, retention time.Duration, logger *log.Logger) *memoryPersister {
	return &memoryPersister{dir: dir, rawWindow: rawWindow, retention: retention, logger: logger}
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

	this.accumulateLocked(sample, now)
}

// accumulateLocked folds the sample into the current minute, flushing the
// previous minute's rollup when the wall-clock minute rolls over. Caller holds
// mu. The hot path only appends; the fold is in-memory until the minute closes.
func (this *memoryPersister) accumulateLocked(sample memorySample, now time.Time) {
	minute := now.UTC().Truncate(time.Minute)
	if this.acc != nil && !this.acc.minute.Equal(minute) {
		this.flushRollupLocked()
	}
	if this.acc == nil {
		this.acc = newMinuteAccumulator(minute)
	}
	this.acc.add(sample)
}

// flushRollupLocked appends the accumulated minute as a rollup line and clears
// the accumulator. Caller holds mu.
func (this *memoryPersister) flushRollupLocked() {
	if this.acc == nil {
		return
	}
	rollup := this.acc.rollup()
	this.acc = nil

	line, err := json.Marshal(rollup)
	if err != nil {
		this.warn("marshal rollup: %v", err)
		return
	}
	if err := os.MkdirAll(this.dir, 0755); err != nil {
		this.warn("mkdir %s: %v", this.dir, err)
		return
	}
	day, _, _ := strings.Cut(rollup.Minute, "T")
	path := filepath.Join(this.dir, "rollups-"+day+".ndjson")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		this.warn("open %s: %v", path, err)
		return
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		this.warn("append rollup: %v", err)
	}
	_ = f.Close()
}

// readRollupSeries reads the rollup tier for one component at or after since.
func (this *memoryPersister) readRollupSeries(name string, since time.Time) []seriesPoint {
	if this == nil {
		return nil
	}
	return readRollupSeries(this.dir, name, since)
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

// pruneLocked enforces the two retention tiers: raw sample files are kept for
// raw_window, rollup files for the full retention. Caller holds mu.
func (this *memoryPersister) pruneLocked(now time.Time) {
	entries, err := os.ReadDir(this.dir)
	if err != nil {
		return // dir not created yet — nothing to prune
	}
	rawCutoff := now.UTC().Add(-this.rawWindow)
	rollupCutoff := now.UTC().Add(-this.retention)
	for _, e := range entries {
		name := e.Name()
		switch {
		case this.rawWindow > 0 && strings.HasPrefix(name, "samples-"):
			this.pruneDayFile(name, "samples-", rawCutoff)
		case this.retention > 0 && strings.HasPrefix(name, "rollups-"):
			this.pruneDayFile(name, "rollups-", rollupCutoff)
		}
	}
}

// pruneDayFile removes a per-day NDJSON file whose day ends before cutoff.
func (this *memoryPersister) pruneDayFile(name, prefix string, cutoff time.Time) {
	datePart, ok := strings.CutPrefix(name, prefix)
	if !ok || !strings.HasSuffix(datePart, ".ndjson") {
		return
	}
	datePart = strings.TrimSuffix(datePart, ".ndjson")
	day, err := time.Parse("2006-01-02", datePart)
	if err != nil {
		return
	}
	// Compare against the end of that day so a same-day boundary is kept.
	if day.AddDate(0, 0, 1).Before(cutoff) {
		_ = os.Remove(filepath.Join(this.dir, name))
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
