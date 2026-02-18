package glob

import (
	"fmt"
	"os"

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

	fsys := os.DirFS(root)

	for _, p := range patterns {
		if p.Negated {
			continue
		}
		matches, err := doublestar.Glob(fsys, p.Raw)
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
		matches, err := doublestar.Glob(fsys, p.Raw)
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

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
