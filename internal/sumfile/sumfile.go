package sumfile

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Entry represents a single file and its hash in the sum file.
type Entry struct {
	Path string
	Hash string
}

// ChangeSet describes the differences between two sum files.
type ChangeSet struct {
	Added    []string
	Modified []string
	Removed  []string
}

// IsEmpty returns true if there are no changes.
func (this *ChangeSet) IsEmpty() bool {
	return len(this.Added) == 0 && len(this.Modified) == 0 && len(this.Removed) == 0
}

// Read parses a sum file from disk into a map of path->hash.
func Read(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open sum file: %w", err)
	}
	defer f.Close()

	entries := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		entries[parts[0]] = parts[1]
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read sum file: %w", err)
	}
	return entries, nil
}

// Write writes a map of path->hash to a sum file, sorted alphabetically.
func Write(path string, entries map[string]string) error {
	sorted := make([]Entry, 0, len(entries))
	for p, h := range entries {
		sorted = append(sorted, Entry{Path: p, Hash: h})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Path < sorted[j].Path
	})

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create sum file: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, e := range sorted {
		fmt.Fprintf(w, "%s %s\n", e.Path, e.Hash)
	}
	return w.Flush()
}

// Diff compares old and new entry maps and returns a ChangeSet.
func Diff(old, new map[string]string) ChangeSet {
	var cs ChangeSet

	for path, newHash := range new {
		oldHash, exists := old[path]
		if !exists {
			cs.Added = append(cs.Added, path)
		} else if oldHash != newHash {
			cs.Modified = append(cs.Modified, path)
		}
	}

	for path := range old {
		if _, exists := new[path]; !exists {
			cs.Removed = append(cs.Removed, path)
		}
	}

	sort.Strings(cs.Added)
	sort.Strings(cs.Modified)
	sort.Strings(cs.Removed)

	return cs
}
