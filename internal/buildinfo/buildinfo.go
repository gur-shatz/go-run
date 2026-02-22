package buildinfo

import "fmt"

// Set via -ldflags at build time.
var (
	Version = "dev"
	Commit  = "unknown"
	Branch  = "unknown"
	Date    = "unknown"
)

func String() string {
	return fmt.Sprintf("version %s (commit %s, branch %s, built %s)", Version, Commit, Branch, Date)
}
