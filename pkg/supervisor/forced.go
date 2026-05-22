package supervisor

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// ForcedKindStable means the component is pinned to whatever stable.txt currently names.
// ForcedKindVersion means the component is pinned to a specific version string.
type ForcedKind int

const (
	ForcedKindNone ForcedKind = iota
	ForcedKindStable
	ForcedKindVersion
)

// ForcedOverride is a single resolved override entry.
type ForcedOverride struct {
	Kind    ForcedKind
	Version string // only set when Kind == ForcedKindVersion
}

// ForcedOverrides holds the parsed contents of forced_versions.txt. It is a
// snapshot — the supervisor re-reads the file every poll tick.
type ForcedOverrides struct {
	wildcard  ForcedOverride
	overrides map[string]ForcedOverride
}

// Lookup applies precedence: explicit component entries beat the wildcard.
// Returns ForcedKindNone when no override applies.
func (this ForcedOverrides) Lookup(component string) ForcedOverride {
	if o, ok := this.overrides[component]; ok {
		return o
	}
	if this.wildcard.Kind != ForcedKindNone {
		return this.wildcard
	}
	return ForcedOverride{Kind: ForcedKindNone}
}

// HasAny reports whether any override (component or wildcard) is in effect.
func (this ForcedOverrides) HasAny() bool {
	return this.wildcard.Kind != ForcedKindNone || len(this.overrides) > 0
}

// ReadForcedOverrides parses forced_versions.txt at the given path. A missing
// file is not an error and yields an empty ForcedOverrides. Parse errors include
// the offending line number so the operator can find their typo.
func ReadForcedOverrides(path string) (ForcedOverrides, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ForcedOverrides{}, nil
		}
		return ForcedOverrides{}, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	out := ForcedOverrides{overrides: make(map[string]ForcedOverride)}
	sc := bufio.NewScanner(f)
	lineno := 0
	for sc.Scan() {
		lineno++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Trim trailing inline comment.
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
			if line == "" {
				continue
			}
		}

		nameRaw, valueRaw, ok := strings.Cut(line, "=")
		if !ok {
			return ForcedOverrides{}, fmt.Errorf("%s:%d: missing '=' in %q", path, lineno, line)
		}
		name := strings.TrimSpace(nameRaw)
		value := strings.TrimSpace(valueRaw)
		if name == "" {
			return ForcedOverrides{}, fmt.Errorf("%s:%d: empty component name", path, lineno)
		}
		if value == "" {
			return ForcedOverrides{}, fmt.Errorf("%s:%d: empty value for %q", path, lineno, name)
		}

		var override ForcedOverride
		if value == "stable" {
			override = ForcedOverride{Kind: ForcedKindStable}
		} else {
			override = ForcedOverride{Kind: ForcedKindVersion, Version: value}
		}

		if name == "*" {
			out.wildcard = override
			continue
		}
		out.overrides[name] = override
	}
	if err := sc.Err(); err != nil {
		return ForcedOverrides{}, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}
