package supervisor

import "fmt"

// humanBytes renders a byte count in binary units with at most one decimal, for
// portal and log display. Zero renders as "—" so an unresolved/absent figure is
// visually distinct from a real 0 B.
func humanBytes(n int64) string {
	if n <= 0 {
		return "—"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
