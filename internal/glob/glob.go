package glob

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Pattern represents a single glob pattern, either include or exclude.
type Pattern struct {
	Raw     string
	Negated bool
}

// ExpandPatterns expands the patterns relative to the given root directory
// and returns a sorted, deduplicated list of matching file paths (relative to root).
func ExpandPatterns(root string, patterns []Pattern) ([]string, error) {
	includes := make(map[string]bool)

	for _, p := range patterns {
		if p.Negated {
			continue
		}
		matches, err := expandSinglePattern(root, p.Raw)
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", p.Raw, err)
		}
		for _, m := range matches {
			includes[m] = true
		}
	}

	// Apply exclusions
	for _, p := range patterns {
		if !p.Negated {
			continue
		}
		matches, err := expandSinglePattern(root, p.Raw)
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", p.Raw, err)
		}
		for _, m := range matches {
			delete(includes, m)
		}
	}

	result := make([]string, 0, len(includes))
	for path := range includes {
		result = append(result, path)
	}
	sortStrings(result)
	return result, nil
}

// expandSinglePattern handles a single glob pattern. For patterns starting with
// "..", it resolves the directory prefix to an absolute path so os.DirFS can
// access files outside the root, then re-prefixes results so they stay relative
// to root.
func expandSinglePattern(root, pattern string) ([]string, error) {
	if !strings.HasPrefix(pattern, "..") {
		fsys := os.DirFS(root)
		return doublestar.Glob(fsys, pattern)
	}

	// Split into directory prefix and glob part.
	// e.g. "../lib/**/*.go" → dir="../lib", globPart="**/*.go"
	dir, globPart := doublestar.SplitPattern(pattern)

	// Resolve the directory prefix against root to get an absolute path.
	absDir := filepath.Clean(filepath.Join(root, dir))

	fsys := os.DirFS(absDir)
	matches, err := doublestar.Glob(fsys, globPart)
	if err != nil {
		return nil, err
	}

	// Re-prefix results with the original directory part so they remain
	// relative to root (e.g. "../lib/foo.go").
	prefix := filepath.ToSlash(dir)
	for i, m := range matches {
		matches[i] = prefix + "/" + m
	}
	return matches, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
