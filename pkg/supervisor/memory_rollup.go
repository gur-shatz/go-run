package supervisor

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// minuteRollup is one minute of folded samples: per-component min/mean/max of
// observed usage (max_bytes is the peak observed in the minute, distinct from
// limit_bytes which is the budget) plus the pod aggregate. It is the tier the
// detail-page sparkline and longer-range queries read, so a 72h view stays
// cheap regardless of how far back it looks.
type minuteRollup struct {
	Minute     string            `json:"minute"` // RFC3339 truncated to the minute
	Components []componentRollup `json:"components"`
	Pod        podRollup         `json:"pod"`
}

type componentRollup struct {
	Name       string `json:"name"`
	MinBytes   int64  `json:"min_bytes"`
	MeanBytes  int64  `json:"mean_bytes"`
	MaxBytes   int64  `json:"max_bytes"` // peak observed (not the budget)
	HighBytes  int64  `json:"high_bytes,omitempty"`
	LimitBytes int64  `json:"limit_bytes,omitempty"`
	WorstState string `json:"worst_state,omitempty"`
}

type podRollup struct {
	MinBytes  int64 `json:"min_bytes"`
	MeanBytes int64 `json:"mean_bytes"`
	MaxBytes  int64 `json:"max_bytes"`
}

// valAgg accumulates min/mean/max for one series over a minute.
type valAgg struct {
	min, max, sum, n int64
}

func (this *valAgg) add(v int64) {
	if this.n == 0 || v < this.min {
		this.min = v
	}
	if v > this.max {
		this.max = v
	}
	this.sum += v
	this.n++
}

func (this *valAgg) mean() int64 {
	if this.n == 0 {
		return 0
	}
	return this.sum / this.n
}

// compAgg accumulates one component's usage over a minute, plus the (static
// within the minute) budgets and the worst state seen.
type compAgg struct {
	val        valAgg
	high       int64
	limit      int64
	worstState string
}

// minuteAccumulator folds samples sharing one wall-clock minute into a rollup.
// It lives on the persister and flushes when the minute rolls over, so the hot
// path only appends.
type minuteAccumulator struct {
	minute time.Time
	comps  map[string]*compAgg
	order  []string // component names in first-seen order, for stable output
	pod    valAgg
}

func newMinuteAccumulator(minute time.Time) *minuteAccumulator {
	return &minuteAccumulator{minute: minute, comps: map[string]*compAgg{}}
}

// add folds one sample into the accumulator.
func (this *minuteAccumulator) add(s memorySample) {
	this.pod.add(s.Pod.CurrentBytes)
	for _, c := range s.Components {
		ca := this.comps[c.Name]
		if ca == nil {
			ca = &compAgg{}
			this.comps[c.Name] = ca
			this.order = append(this.order, c.Name)
		}
		ca.val.add(c.CurrentBytes)
		ca.high = c.HighBytes
		ca.limit = c.LimitBytes
		if memStateRank(c.State) > memStateRank(ca.worstState) {
			ca.worstState = c.State
		}
	}
}

// rollup materialises the accumulated minute.
func (this *minuteAccumulator) rollup() minuteRollup {
	out := minuteRollup{
		Minute: this.minute.UTC().Format(time.RFC3339),
		Pod:    podRollup{MinBytes: this.pod.min, MeanBytes: this.pod.mean(), MaxBytes: this.pod.max},
	}
	for _, name := range this.order {
		ca := this.comps[name]
		out.Components = append(out.Components, componentRollup{
			Name:       name,
			MinBytes:   ca.val.min,
			MeanBytes:  ca.val.mean(),
			MaxBytes:   ca.val.max,
			HighBytes:  ca.high,
			LimitBytes: ca.limit,
			WorstState: ca.worstState,
		})
	}
	return out
}

// memStateRank orders the assessment states so the worst in a minute wins.
func memStateRank(state string) int {
	switch state {
	case memStateHard:
		return 3
	case memStateSoft:
		return 2
	case memStateOK:
		return 1
	default:
		return 0
	}
}

// readRollupSeries reads minute rollups for one component at or after since,
// oldest first. The representative value is the minute's peak (max_bytes), which
// is what matters for "did it spike". Returns nil when no rollups exist.
func readRollupSeries(dir, name string, since time.Time) []seriesPoint {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []seriesPoint
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "rollups-") || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var r minuteRollup
			if json.Unmarshal(line, &r) != nil {
				continue
			}
			ts, err := time.Parse(time.RFC3339, r.Minute)
			if err != nil || (!since.IsZero() && ts.Before(since)) {
				continue
			}
			for _, c := range r.Components {
				if c.Name != name {
					continue
				}
				out = append(out, seriesPoint{
					TS:           r.Minute,
					CurrentBytes: c.MaxBytes,
					HighBytes:    c.HighBytes,
					LimitBytes:   c.LimitBytes,
					State:        c.WorstState,
				})
			}
		}
		_ = f.Close()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}
