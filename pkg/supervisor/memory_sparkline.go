package supervisor

import (
	"fmt"
	"html/template"
	"strings"
)

// sparkline geometry. Pure server-rendered SVG, no JavaScript, matching the
// portal's static-HTML model. It answers the one question the Hertzner incident
// left open: gradual climb or spike.
const (
	sparkW   = 320
	sparkH   = 56
	sparkPad = 3
)

// memorySparkline renders a component's current-bytes history as an inline SVG.
// When a hard budget is present it draws faint soft/hard threshold lines so the
// climb is readable against the limit. Returns "" for an empty series.
func memorySparkline(points []seriesPoint) template.HTML {
	if len(points) < 2 {
		return ""
	}

	// Vertical scale: the larger of the peak usage and the hard limit, so the
	// budget line stays on-canvas and usage never clips.
	var peak, high, limit int64
	for _, p := range points {
		if p.CurrentBytes > peak {
			peak = p.CurrentBytes
		}
		if p.HighBytes > high {
			high = p.HighBytes
		}
		if p.LimitBytes > limit {
			limit = p.LimitBytes
		}
	}
	top := peak
	if limit > top {
		top = limit
	}
	if top <= 0 {
		return ""
	}

	y := func(v int64) float64 {
		frac := float64(v) / float64(top)
		return float64(sparkH-sparkPad) - frac*float64(sparkH-2*sparkPad)
	}
	x := func(i int) float64 {
		if len(points) == 1 {
			return sparkPad
		}
		return float64(sparkPad) + float64(i)/float64(len(points)-1)*float64(sparkW-2*sparkPad)
	}

	var coords strings.Builder
	for i, p := range points {
		if i > 0 {
			coords.WriteByte(' ')
		}
		fmt.Fprintf(&coords, "%.1f,%.1f", x(i), y(p.CurrentBytes))
	}

	// Stroke colour follows the latest assessed state.
	stroke := "var(--pass)"
	switch points[len(points)-1].State {
	case memStateSoft:
		stroke = "var(--warn)"
	case memStateHard:
		stroke = "var(--fail)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" preserveAspectRatio="none" width="100%%" height="%d" role="img" aria-label="memory history">`, sparkW, sparkH, sparkH)
	if limit > 0 {
		fmt.Fprintf(&b, `<line x1="0" y1="%.1f" x2="%d" y2="%.1f" stroke="var(--fail)" stroke-width="1" stroke-dasharray="3 3" opacity="0.45"/>`, y(limit), sparkW, y(limit))
	}
	if high > 0 {
		fmt.Fprintf(&b, `<line x1="0" y1="%.1f" x2="%d" y2="%.1f" stroke="var(--warn)" stroke-width="1" stroke-dasharray="3 3" opacity="0.45"/>`, y(high), sparkW, y(high))
	}
	fmt.Fprintf(&b, `<polyline fill="none" stroke="%s" stroke-width="1.5" stroke-linejoin="round" points="%s"/>`, stroke, coords.String())
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
