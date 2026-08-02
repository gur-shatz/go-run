package supervisor

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gur-shatz/statekit"
)

// gcResult summarises the outcome of one CleanOrphanVersions call.
type gcResult struct {
	Kept    []string
	Deleted []string
}

// VersionGCPolicy bounds what CleanOrphanVersions may delete. Both conditions
// must hold before an orphan folder goes: it must fall outside the newest
// Retain orphans, and it must be older than MinAge. The age floor is what
// makes the sweep safe to run against a live supervisor — a folder being
// extracted or just promoted is minutes old, far inside the window.
type VersionGCPolicy struct {
	// Retain is how many of the newest orphans (by mtime) survive regardless
	// of age, so a manual rollback to a recent version stays possible.
	Retain int

	// MinAge is the minimum time since last modification before an orphan is
	// eligible for deletion. Zero means no age floor.
	MinAge time.Duration
}

// versionGCPolicy is the sweep policy implied by the resolved config.
func (this Config) versionGCPolicy() VersionGCPolicy {
	return VersionGCPolicy{
		Retain: this.VersionFolderRetention,
		MinAge: this.VersionFolderMinAge,
	}
}

// CleanOrphanVersions deletes obsolete version folders under
// state_dir/<component>/versions/. A folder is an orphan when its name is not
// referenced by stable.txt, current.txt, or rejects.txt. Orphans are removed
// only when the policy allows — see VersionGCPolicy.
func CleanOrphanVersions(paths ComponentPaths, policy VersionGCPolicy) (gcResult, error) {
	return cleanOrphanVersions(paths, policy, time.Now())
}

func cleanOrphanVersions(paths ComponentPaths, policy VersionGCPolicy, now time.Time) (gcResult, error) {
	versionsDir := paths.Versions()
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return gcResult{}, nil
		}
		return gcResult{}, fmt.Errorf("read %s: %w", versionsDir, err)
	}

	stable, _ := paths.ReadStable()
	current, _ := paths.ReadCurrent()
	rejects, _ := paths.ReadRejects()
	referenced := append([]string{stable, current}, rejects...)

	type folder struct {
		name  string
		mtime time.Time
	}
	var orphans []folder
	var kept []string

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "" || slices.Contains(referenced, name) {
			kept = append(kept, name)
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		orphans = append(orphans, folder{name: name, mtime: info.ModTime()})
	}

	// Newest first.
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].mtime.After(orphans[j].mtime) })

	retain := policy.Retain
	if retain < 0 {
		retain = 0
	}
	if retain > len(orphans) {
		retain = len(orphans)
	}
	for i := 0; i < retain; i++ {
		kept = append(kept, orphans[i].name)
	}

	var doomed []folder
	for _, o := range orphans[retain:] {
		if policy.MinAge > 0 && now.Sub(o.mtime) < policy.MinAge {
			kept = append(kept, o.name)
			continue
		}
		doomed = append(doomed, o)
	}

	res := gcResult{Kept: kept}
	for _, o := range doomed {
		full := filepath.Join(versionsDir, o.name)
		if err := os.RemoveAll(full); err != nil {
			return res, fmt.Errorf("remove %s: %w", full, err)
		}
		// Drop the matching logs alongside the version. Logs live outside the
		// version dir so they survive ad-hoc inspection of the extracted tree,
		// but once the version itself is gone there is no useful audit trail
		// left to preserve.
		logDir := paths.LogsDir(o.name)
		if err := os.RemoveAll(logDir); err != nil {
			return res, fmt.Errorf("remove %s: %w", logDir, err)
		}
		if err := removeRotatedLogFiles(paths.Log(o.name)); err != nil {
			return res, err
		}
		res.Deleted = append(res.Deleted, o.name)
	}
	return res, nil
}

// dirSize sums the apparent size of every regular file under root. Errors on
// individual entries are skipped and a missing root reports 0: the figure is
// diagnostics, and a folder the sweep just deleted must not turn into a sweep
// failure.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

// versionGCReport is the state of the orphan sweep as of the last pass: what
// the pass itself did, the cumulative totals, and the last error seen in this
// process. Rendered into the "versions.gc" statekit state.
type versionGCReport struct {
	At           time.Time
	Runs         int64
	Deleted      int
	DeletedTotal int64
	Bytes        int64
	Components   map[string]int64 // component -> versions/ size in bytes
	LastError    string
	LastErrorAt  time.Time
}

// versionGCTracker accumulates sweep outcomes across runs. The last error is
// sticky: it survives later clean passes so an operator can still see that a
// sweep failed at some point, with the state itself back at pass.
type versionGCTracker struct {
	mu           sync.Mutex
	runs         int64
	deletedTotal int64
	report       versionGCReport
}

// record folds one pass into the tracker and returns the resulting report.
// sizes maps component name to the size of its versions/ folder after the
// pass; errText is empty on a clean pass.
func (this *versionGCTracker) record(at time.Time, deleted int, sizes map[string]int64, errText string) versionGCReport {
	this.mu.Lock()
	defer this.mu.Unlock()

	this.runs++
	this.deletedTotal += int64(deleted)

	var bytes int64
	for _, s := range sizes {
		bytes += s
	}

	rep := versionGCReport{
		At:           at,
		Runs:         this.runs,
		Deleted:      deleted,
		DeletedTotal: this.deletedTotal,
		Bytes:        bytes,
		Components:   sizes,
		LastError:    this.report.LastError,
		LastErrorAt:  this.report.LastErrorAt,
	}
	if errText != "" {
		rep.LastError = errText
		rep.LastErrorAt = at
	}
	this.report = rep
	return rep
}

// snapshot returns the last recorded report (zero value before the first pass).
func (this *versionGCTracker) snapshot() versionGCReport {
	this.mu.Lock()
	defer this.mu.Unlock()
	return this.report
}

// versionGCLiveState wraps the versions.gc leaf so the "ago" fields are
// computed when the state is read rather than when the sweep wrote it. With a
// daily cadence a value frozen at write time would read "just now" for the
// following 24 hours, which is precisely the number an operator is checking.
type versionGCLiveState struct {
	underlying *statekit.ManualState
	tracker    *versionGCTracker
}

func (this *versionGCLiveState) Name() string { return this.underlying.Name() }

func (this *versionGCLiveState) Snapshot() statekit.Snapshot {
	snap := this.underlying.Snapshot()
	rep := this.tracker.snapshot()
	if rep.At.IsZero() {
		return snap
	}
	data := make(map[string]any, len(snap.Data)+2)
	maps.Copy(data, snap.Data)
	data["last_run_ago"] = agoSince(rep.At)
	if !rep.LastErrorAt.IsZero() {
		data["last_error_ago"] = agoSince(rep.LastErrorAt)
	}
	snap.Data = data
	return snap
}

func removeRotatedLogFiles(path string) error {
	matches, err := filepath.Glob(path + "*")
	if err != nil {
		return fmt.Errorf("glob %s*: %w", path, err)
	}
	for _, match := range matches {
		name := filepath.Base(match)
		base := filepath.Base(path)
		if name != base && !strings.HasPrefix(name, base+".") {
			continue
		}
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", match, err)
		}
	}
	return nil
}
