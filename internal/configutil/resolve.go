package configutil

import (
	"os"
	"strings"
)

// ResolveYAMLPath checks if the given config path exists. If it doesn't and
// ends with ".yaml", it tries the ".yml" variant (and vice versa). This allows
// users to use either extension for their config files.
func ResolveYAMLPath(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}

	if base, ok := strings.CutSuffix(path, ".yaml"); ok {
		alt := base + ".yml"
		if _, err := os.Stat(alt); err == nil {
			return alt
		}
	} else if base, ok := strings.CutSuffix(path, ".yml"); ok {
		alt := base + ".yaml"
		if _, err := os.Stat(alt); err == nil {
			return alt
		}
	}

	return path
}
