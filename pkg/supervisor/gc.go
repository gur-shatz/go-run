package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// gcResult summarises the outcome of one CleanOrphanVersions call.
type gcResult struct {
	Kept    []string
	Deleted []string
}

// CleanOrphanVersions deletes obsolete version folders under
// state_dir/<component>/versions/. A folder is an orphan when its name is not
// referenced by stable.txt, current.txt, or rejects.txt. The newest `retain`
// orphans are kept (by mtime) so a manual rollback to a previous version is
// still possible; the rest are removed.
func CleanOrphanVersions(paths ComponentPaths, retain int) (gcResult, error) {
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
		mtime int64
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
		orphans = append(orphans, folder{name: name, mtime: info.ModTime().UnixNano()})
	}

	// Newest first.
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].mtime > orphans[j].mtime })

	if retain < 0 {
		retain = 0
	}
	if retain > len(orphans) {
		retain = len(orphans)
	}
	for i := 0; i < retain; i++ {
		kept = append(kept, orphans[i].name)
	}

	res := gcResult{Kept: kept}
	for _, o := range orphans[retain:] {
		full := filepath.Join(versionsDir, o.name)
		if err := os.RemoveAll(full); err != nil {
			return res, fmt.Errorf("remove %s: %w", full, err)
		}
		res.Deleted = append(res.Deleted, o.name)
	}
	return res, nil
}
