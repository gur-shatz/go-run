package watcher

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/fsnotify/fsnotify"

	"github.com/gur-shatz/go-run/internal/glob"
	"github.com/gur-shatz/go-run/internal/hasher"
	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/internal/sumfile"
)

const refreshInterval = 60 * time.Second

// fileStat holds the cached stat info for a file, used to skip
// re-hashing files whose mtime and size haven't changed.
type fileStat struct {
	modTime time.Time
	size    int64
}

// OnChangeFunc is called when file changes are detected.
type OnChangeFunc func(changes sumfile.ChangeSet)

// Watcher uses fsnotify to detect file changes and triggers rebuilds.
type Watcher struct {
	rootDir      string
	patterns     []glob.Pattern
	pollInterval time.Duration
	debounce     time.Duration
	onChange     OnChangeFunc
	log          *log.Logger

	currentSums  map[string]string
	statCache    map[string]fileStat
	trackedFiles map[string]bool
	trackedDirs  map[string]bool
	fsw          *fsnotify.Watcher
	dirty        bool
}

// New creates a new Watcher.
func New(rootDir string, patterns []glob.Pattern, pollInterval, debounce time.Duration, onChange OnChangeFunc, logger *log.Logger) *Watcher {
	return &Watcher{
		rootDir:      rootDir,
		patterns:     patterns,
		pollInterval: pollInterval,
		debounce:     debounce,
		onChange:     onChange,
		log:          logger,
	}
}

// SetCurrentSums sets the initial state of file hashes (from the initial build)
// and populates the stat cache so the first poll tick can skip unchanged files.
func (this *Watcher) SetCurrentSums(sums map[string]string) {
	this.currentSums = sums

	this.statCache = make(map[string]fileStat, len(sums))
	for f := range sums {
		info, err := os.Stat(this.rootDir + "/" + f)
		if err != nil {
			continue
		}
		this.statCache[f] = fileStat{modTime: info.ModTime(), size: info.Size()}
	}
}

// Run starts the watch loop. Blocks until the context is cancelled.
func (this *Watcher) Run(ctx context.Context) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		this.log.Error("fsnotify init failed: %v, falling back to polling", err)
		this.runPollOnly(ctx)
		return
	}
	this.fsw = fsw
	defer this.fsw.Close()

	if err := this.buildFileList(); err != nil {
		this.log.Error("buildFileList failed: %v", err)
		return
	}

	this.log.Verbose("Watching %d directories via fsnotify", len(this.trackedDirs))

	pollTicker := time.NewTicker(this.pollInterval)
	defer pollTicker.Stop()

	refreshTicker := time.NewTicker(refreshInterval)
	defer refreshTicker.Stop()

	var debounceTimer *time.Timer
	var pendingChanges *sumfile.ChangeSet

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case event, ok := <-this.fsw.Events:
			if !ok {
				return
			}
			if event.Op == fsnotify.Chmod {
				continue
			}
			rel, err := filepath.Rel(this.rootDir, event.Name)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if this.trackedFiles[rel] || this.matchesPatterns(rel) {
				this.dirty = true
			}
			// Watch newly created directories
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					this.maybeWatchDir(event.Name)
				}
			}

		case _, ok := <-this.fsw.Errors:
			if !ok {
				return
			}
			// On any fsnotify error (including overflow), force a scan
			this.dirty = true

		case <-pollTicker.C:
			if !this.dirty {
				continue
			}
			this.dirty = false

			newSums, err := this.scan()
			if err != nil {
				continue
			}

			changes := sumfile.Diff(this.currentSums, newSums)
			if changes.IsEmpty() {
				continue
			}

			this.currentSums = newSums

			if pendingChanges == nil {
				pendingChanges = &changes
			} else {
				pendingChanges = mergeChanges(pendingChanges, &changes)
			}

			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(this.debounce, func() {
				if pendingChanges != nil && !pendingChanges.IsEmpty() {
					this.onChange(*pendingChanges)
					pendingChanges = nil
				}
			})

		case <-refreshTicker.C:
			if err := this.buildFileList(); err != nil {
				this.log.Verbose("refresh buildFileList failed: %v", err)
				continue
			}
			this.log.Verbose("Refreshed file list: %d files, %d directories", len(this.trackedFiles), len(this.trackedDirs))
			this.dirty = true
		}
	}
}

// runPollOnly is the fallback when fsnotify is unavailable.
func (this *Watcher) runPollOnly(ctx context.Context) {
	ticker := time.NewTicker(this.pollInterval)
	defer ticker.Stop()

	var debounceTimer *time.Timer
	var pendingChanges *sumfile.ChangeSet

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case <-ticker.C:
			newSums, err := this.scanWithGlob()
			if err != nil {
				continue
			}

			changes := sumfile.Diff(this.currentSums, newSums)
			if changes.IsEmpty() {
				continue
			}

			this.currentSums = newSums

			if pendingChanges == nil {
				pendingChanges = &changes
			} else {
				pendingChanges = mergeChanges(pendingChanges, &changes)
			}

			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(this.debounce, func() {
				if pendingChanges != nil && !pendingChanges.IsEmpty() {
					this.onChange(*pendingChanges)
					pendingChanges = nil
				}
			})
		}
	}
}

// buildFileList expands globs to determine tracked files and directories,
// then syncs fsnotify watches to match.
func (this *Watcher) buildFileList() error {
	files, err := glob.ExpandPatterns(this.rootDir, this.patterns)
	if err != nil {
		return err
	}

	newTrackedFiles := make(map[string]bool, len(files))
	newTrackedDirs := make(map[string]bool)
	// Always watch the root directory
	newTrackedDirs["."] = true

	for _, f := range files {
		newTrackedFiles[f] = true
		dir := filepath.Dir(f)
		for dir != "." {
			newTrackedDirs[dir] = true
			dir = filepath.Dir(dir)
		}
	}

	// Sync fsnotify watches
	if this.fsw != nil {
		// Remove stale watches
		if this.trackedDirs != nil {
			for dir := range this.trackedDirs {
				if !newTrackedDirs[dir] {
					absDir := filepath.Join(this.rootDir, dir)
					this.fsw.Remove(absDir)
				}
			}
		}

		// Add new watches
		for dir := range newTrackedDirs {
			if this.trackedDirs == nil || !this.trackedDirs[dir] {
				absDir := filepath.Join(this.rootDir, dir)
				if err := this.fsw.Add(absDir); err != nil {
					this.log.Warn("watch %s: %v", dir, err)
				} else {
					this.log.Status("Watching directory: %s (%s)", dir, absDir)
				}
			}
		}
	}

	this.trackedFiles = newTrackedFiles
	this.trackedDirs = newTrackedDirs
	return nil
}

// scan hashes the known tracked files, using stat to skip unchanged files.
// Does not walk the filesystem — iterates the known set from buildFileList.
func (this *Watcher) scan() (map[string]string, error) {
	newStatCache := make(map[string]fileStat, len(this.trackedFiles))
	sums := make(map[string]string, len(this.trackedFiles))

	for f := range this.trackedFiles {
		fullPath := this.rootDir + "/" + f

		info, err := os.Stat(fullPath)
		if err != nil {
			continue // file may have been deleted
		}

		st := fileStat{modTime: info.ModTime(), size: info.Size()}
		newStatCache[f] = st

		if prev, ok := this.statCache[f]; ok && prev == st {
			if hash, ok := this.currentSums[f]; ok {
				sums[f] = hash
				continue
			}
		}

		hash, err := hasher.HashFile(fullPath)
		if err != nil {
			continue
		}
		sums[f] = hash
	}

	this.statCache = newStatCache
	return sums, nil
}

// scanWithGlob is the original scan that expands globs. Used as fallback
// when fsnotify is unavailable.
func (this *Watcher) scanWithGlob() (map[string]string, error) {
	files, err := glob.ExpandPatterns(this.rootDir, this.patterns)
	if err != nil {
		return nil, err
	}

	newStatCache := make(map[string]fileStat, len(files))
	sums := make(map[string]string, len(files))

	for _, f := range files {
		fullPath := this.rootDir + "/" + f

		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}

		st := fileStat{modTime: info.ModTime(), size: info.Size()}
		newStatCache[f] = st

		if prev, ok := this.statCache[f]; ok && prev == st {
			if hash, ok := this.currentSums[f]; ok {
				sums[f] = hash
				continue
			}
		}

		hash, err := hasher.HashFile(fullPath)
		if err != nil {
			continue
		}
		sums[f] = hash
	}

	this.statCache = newStatCache
	return sums, nil
}

// matchesPatterns checks if a relative path matches any of the include glob patterns.
// Used for detecting newly created files that aren't yet in trackedFiles.
func (this *Watcher) matchesPatterns(rel string) bool {
	for _, p := range this.patterns {
		if p.Negated {
			continue
		}
		if matched, _ := doublestar.Match(p.Raw, rel); matched {
			return true
		}
	}
	return false
}

// maybeWatchDir adds an fsnotify watch to a newly created directory if it's
// under a tracked directory (one that matched a watch pattern).
func (this *Watcher) maybeWatchDir(absPath string) {
	rel, err := filepath.Rel(this.rootDir, absPath)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)

	// Only watch if this directory or a parent is already tracked.
	tracked := false
	if this.trackedDirs[rel] {
		tracked = true
	} else {
		parent := rel
		for parent != "." && parent != "/" {
			parent = filepath.ToSlash(filepath.Dir(parent))
			if this.trackedDirs[parent] {
				tracked = true
				break
			}
		}
	}
	if !tracked {
		return
	}

	if this.fsw != nil {
		if err := this.fsw.Add(absPath); err == nil {
			this.trackedDirs[rel] = true
			this.log.Status("Watching new directory: %s (%s)", rel, absPath)
		}
	}
}

// mergeChanges combines two changesets.
func mergeChanges(a, b *sumfile.ChangeSet) *sumfile.ChangeSet {
	seen := make(map[string]bool)
	result := &sumfile.ChangeSet{}

	for _, f := range append(a.Added, b.Added...) {
		if !seen[f] {
			result.Added = append(result.Added, f)
			seen[f] = true
		}
	}
	for _, f := range append(a.Modified, b.Modified...) {
		if !seen[f] {
			result.Modified = append(result.Modified, f)
			seen[f] = true
		}
	}
	for _, f := range append(a.Removed, b.Removed...) {
		if !seen[f] {
			result.Removed = append(result.Removed, f)
			seen[f] = true
		}
	}
	return result
}
