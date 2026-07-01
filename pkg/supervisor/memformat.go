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

// percentString renders a 0..1 ratio as a whole-number percentage, e.g. 0.91 ->
// "91%". Used in pod-pressure incident reasons and logs.
func percentString(ratio float64) string {
	return fmt.Sprintf("%.0f%%", ratio*100)
}

// bytesToMiB converts a byte count to whole MiB, rounded to nearest, for the
// human-readable metrics carried on the memory state leaf. Negative or zero maps
// to 0.
func bytesToMiB(b int64) int64 {
	if b <= 0 {
		return 0
	}
	const mib = 1 << 20
	return (b + mib/2) / mib
}
