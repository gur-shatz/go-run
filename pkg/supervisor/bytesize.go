package supervisor

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ByteSize is a memory size in bytes that accepts, in YAML, either a plain
// integer (bytes) or a human string with a binary-unit suffix — k/m/g/t =
// KiB/MiB/GiB/TiB, e.g. "10m" = 10*1024*1024. It is case-insensitive and
// tolerates an optional trailing "b" or "ib" ("10MiB", "10MB", "10m" all mean
// the same). A fractional value is allowed ("1.5g"). It exists so component
// memory budgets can be written as `hardlimit: 10m` rather than a raw byte count.
type ByteSize int64

// UnmarshalYAML parses a scalar node (string or integer) into bytes.
func (this *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	v, err := parseByteSize(s)
	if err != nil {
		return fmt.Errorf("invalid byte size %q: %w", s, err)
	}
	*this = ByteSize(v)
	return nil
}

// parseByteSize converts "10m", "512k", "1.5g", "10485760", "10MiB" to bytes
// using binary units. An empty string is 0.
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}
	// Strip an optional "ib"/"b" tail so "10mib", "10mb", "10m" are equivalent.
	s = strings.TrimSuffix(s, "ib")
	s = strings.TrimSuffix(s, "b")

	mult := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'k':
			mult, s = 1<<10, s[:n-1]
		case 'm':
			mult, s = 1<<20, s[:n-1]
		case 'g':
			mult, s = 1<<30, s[:n-1]
		case 't':
			mult, s = 1<<40, s[:n-1]
		}
	}
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, fmt.Errorf("must be non-negative")
	}
	return int64(v * float64(mult)), nil
}
