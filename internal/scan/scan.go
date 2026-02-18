package scan

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/gur-shatz/go-run/internal/glob"
	"github.com/gur-shatz/go-run/internal/hasher"
)

// ParseWatchPatterns converts string patterns to glob.Pattern slice.
// Patterns prefixed with "!" are treated as negation (exclusion) patterns.
func ParseWatchPatterns(watch []string) []glob.Pattern {
	patterns := make([]glob.Pattern, 0, len(watch))
	for _, w := range watch {
		if len(w) > 0 && w[0] == '!' {
			patterns = append(patterns, glob.Pattern{Raw: w[1:], Negated: true})
		} else {
			patterns = append(patterns, glob.Pattern{Raw: w})
		}
	}
	return patterns
}

// ScanFiles expands watch patterns and hashes all matching files.
// Returns a map of relative path → hash.
func ScanFiles(rootDir string, patterns []glob.Pattern) (map[string]string, error) {
	files, err := glob.ExpandPatterns(rootDir, patterns)
	if err != nil {
		return nil, err
	}

	sums := make(map[string]string, len(files))
	for _, f := range files {
		hash, err := hasher.HashFile(filepath.Join(rootDir, f))
		if err != nil {
			continue
		}
		sums[f] = hash
	}
	return sums, nil
}

// FormatDuration formats a duration as seconds with one decimal place.
func FormatDuration(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}
