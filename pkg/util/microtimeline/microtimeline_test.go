package microtimeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestTimelineKeepsNewestEntriesWithinByteBudget(t *testing.T) {
	tl, err := New(28)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(0, 0)

	mustAppendAt(t, tl, base, "aaaa")
	mustAppendAt(t, tl, base.Add(time.Millisecond), "bbbb")
	mustAppendAt(t, tl, base.Add(2*time.Millisecond), "cccc")

	got, err := tl.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Message != "bbbb" || got[1].Message != "cccc" {
		t.Fatalf("messages = %#v", got)
	}
}

func TestTimelineWrapsRecordsWithoutSplittingThem(t *testing.T) {
	tl, err := New(31)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(0, 0)

	mustAppendAt(t, tl, base, "first")
	mustAppendAt(t, tl, base.Add(time.Millisecond), "second")
	mustAppendAt(t, tl, base.Add(2*time.Millisecond), "third")

	got, err := tl.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Message != "second" || got[1].Message != "third" {
		t.Fatalf("messages = %#v", got)
	}
}

func TestTimelineTruncatesOversizedMessage(t *testing.T) {
	tl, err := New(10)
	if err != nil {
		t.Fatal(err)
	}

	mustAppendAt(t, tl, time.Unix(0, 0), "abcdef")

	got, err := tl.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Message != "abcd" {
		t.Fatalf("message = %q, want %q", got[0].Message, "abcd")
	}
	if !got[0].Truncated {
		t.Fatal("entry was not marked truncated")
	}
}

func TestTimelineChoosesCompactOffsetUnit(t *testing.T) {
	tl, err := New(128)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(0, 0)

	mustAppendAt(t, tl, base, "zero")
	mustAppendAt(t, tl, base.Add(100*time.Microsecond), "micro")
	mustAppendAt(t, tl, base.Add(2*time.Second), "milli")
	mustAppendAt(t, tl, base.Add(3*time.Hour), "second")

	got, err := tl.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	checks := []struct {
		i    int
		unit string
		d    time.Duration
	}{
		{0, "ns", 0},
		{1, "us", 100 * time.Microsecond},
		{2, "ms", 1999 * time.Millisecond},
		{3, "s", 10798 * time.Second},
	}
	for _, check := range checks {
		if got[check.i].OffsetUnit != check.unit || got[check.i].Offset != check.d {
			t.Fatalf("entry %d = unit %s offset %s, want %s %s", check.i, got[check.i].OffsetUnit, got[check.i].Offset, check.unit, check.d)
		}
	}
}

func TestTimelineOutputFormats(t *testing.T) {
	tl, err := New(64)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(0, 0)
	mustAppendAt(t, tl, base, "hello")
	mustAppendAt(t, tl, base.Add(time.Millisecond), "world")

	var jsonOut bytes.Buffer
	if err := tl.WriteJSON(&jsonOut); err != nil {
		t.Fatal(err)
	}
	var decoded []Entry
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 || decoded[1].Message != "world" {
		t.Fatalf("json decoded = %#v", decoded)
	}

	var yamlOut bytes.Buffer
	if err := tl.WriteYAML(&yamlOut); err != nil {
		t.Fatal(err)
	}
	var yamlLines []string
	if err := yaml.Unmarshal(yamlOut.Bytes(), &yamlLines); err != nil {
		t.Fatalf("yaml output is not a valid string sequence: %v\n%s", err, yamlOut.String())
	}
	if len(yamlLines) != 2 || !strings.Contains(yamlLines[1], "world") || !strings.Contains(yamlLines[1], "ago)") {
		t.Fatalf("yaml lines = %#v", yamlLines)
	}

	var textOut bytes.Buffer
	if err := tl.WriteText(&textOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOut.String(), "ago) world") {
		t.Fatalf("text output = %s", textOut.String())
	}
}

func TestTimelineWriteMarkdownEscapesTableCells(t *testing.T) {
	tl, err := New(256)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Minute)
	mustAppendAt(t, tl, base, "plain event")
	mustAppendAt(t, tl, base.Add(time.Second), "pipe | and\nnewline")
	mustAppendAt(t, tl, base.Add(2*time.Second), "code `span` inside")

	var out bytes.Buffer
	if err := tl.WriteMarkdown(&out); err != nil {
		t.Fatal(err)
	}
	md := out.String()
	if !strings.HasPrefix(md, "| Time | Ago | Event |\n| --- | --- | --- |\n") {
		t.Fatalf("markdown missing table header:\n%s", md)
	}
	if !strings.Contains(md, "`plain event`") {
		t.Fatalf("markdown missing plain row:\n%s", md)
	}
	if !strings.Contains(md, `pipe \| and newline`) {
		t.Fatalf("pipe/newline not escaped:\n%s", md)
	}
	if !strings.Contains(md, "`` code `span` inside ``") {
		t.Fatalf("backtick message not double-delimited:\n%s", md)
	}
	if got := strings.Count(md, "\n"); got != 5 {
		t.Fatalf("markdown line count = %d, want 5 (header + separator + 3 rows):\n%s", got, md)
	}
}

func TestTimelineSnapshotReconstructsAbsoluteTimes(t *testing.T) {
	tl, err := New(128)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	mustAppendAt(t, tl, base, "first")
	mustAppendAt(t, tl, base.Add(250*time.Millisecond), "second")
	mustAppendAt(t, tl, base.Add(10*time.Second), "third")

	got, err := tl.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// The newest entry is anchored exactly at the last append; earlier
	// entries drift by at most one storage unit per hop.
	if !got[2].At.Equal(base.Add(10 * time.Second)) {
		t.Fatalf("newest At = %s, want %s", got[2].At, base.Add(10*time.Second))
	}
	for i, want := range []time.Time{base, base.Add(250 * time.Millisecond)} {
		diff := got[i].At.Sub(want)
		if diff < 0 {
			diff = -diff
		}
		if diff > 10*time.Millisecond {
			t.Fatalf("entry %d At = %s, want within 10ms of %s", i, got[i].At, want)
		}
	}
	line := got[2].Render(base.Add(10*time.Second + 23*time.Second))
	if !strings.Contains(line, "(23s ago) third") {
		t.Fatalf("rendered line = %q", line)
	}
}

func TestTimelineAppendlnFormatsLikePrintln(t *testing.T) {
	tl, err := New(64)
	if err != nil {
		t.Fatal(err)
	}

	if !tl.Appendln("worker", 12, "ready") {
		t.Fatal("Appendln failed")
	}

	got, err := tl.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Message != "worker 12 ready" {
		t.Fatalf("message = %q, want %q", got[0].Message, "worker 12 ready")
	}
}

func TestTimelineAppendfFormatsLikePrintf(t *testing.T) {
	tl, err := New(64)
	if err != nil {
		t.Fatal(err)
	}

	if !tl.Appendf("worker-%02d %s", 7, "ready") {
		t.Fatal("Appendf failed")
	}

	got, err := tl.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Message != "worker-07 ready" {
		t.Fatalf("message = %q, want %q", got[0].Message, "worker-07 ready")
	}
}

func TestTimelineConcurrentAppendAndSnapshot(t *testing.T) {
	tl, err := New(4096)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = tl.Append("event")
				_, _ = tl.Snapshot()
			}
		}()
	}
	wg.Wait()

	got, err := tl.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("snapshot is empty after appends")
	}
}

func TestTimelineConcurrentOrderedAppendsRetainSequence(t *testing.T) {
	const workers = 8
	const messages = 1000

	tl, err := New(211)
	if err != nil {
		t.Fatal(err)
	}

	stopSnapshots := make(chan struct{})
	var snapshots sync.WaitGroup
	snapshots.Add(1)
	go func() {
		defer snapshots.Done()
		for {
			select {
			case <-stopSnapshots:
				return
			default:
				_, _ = tl.Snapshot()
				runtime.Gosched()
			}
		}
	}()

	var turn atomic.Int64
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for seq := worker; seq < messages; seq += workers {
				for turn.Load() != int64(seq) {
					runtime.Gosched()
				}
				msg := fmt.Sprintf("msg-%06d", seq)
				for !tl.Append(msg) {
					runtime.Gosched()
				}
				turn.Add(1)
			}
		}(worker)
	}
	wg.Wait()
	close(stopSnapshots)
	snapshots.Wait()

	got, err := tl.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("snapshot is empty after appends")
	}

	prev := -1
	for i, entry := range got {
		if !strings.HasPrefix(entry.Message, "msg-") {
			t.Fatalf("entry %d message = %q, want msg-*", i, entry.Message)
		}
		seq, err := strconv.Atoi(strings.TrimPrefix(entry.Message, "msg-"))
		if err != nil {
			t.Fatalf("entry %d message = %q: %v", i, entry.Message, err)
		}
		if i == 0 {
			prev = seq
			continue
		}
		if seq != prev+1 {
			t.Fatalf("entry %d sequence = %d, previous = %d; entries = %#v", i, seq, prev, got)
		}
		prev = seq
	}
	if prev != messages-1 {
		t.Fatalf("last retained sequence = %d, want %d", prev, messages-1)
	}
}

func TestTimelineFailsInsteadOfBlockingWhenBusy(t *testing.T) {
	tl, err := New(64)
	if err != nil {
		t.Fatal(err)
	}
	tl.mu.Lock()
	defer tl.mu.Unlock()

	if tl.Append("event") {
		t.Fatal("Append succeeded while timeline lock was held")
	}
	if _, err := tl.Snapshot(); err != ErrBusy {
		t.Fatalf("Snapshot err = %v, want %v", err, ErrBusy)
	}
	var out bytes.Buffer
	if err := tl.WriteJSON(&out); err != ErrBusy {
		t.Fatalf("WriteJSON err = %v, want %v", err, ErrBusy)
	}
}

func mustAppendAt(t *testing.T, tl *Timeline, at time.Time, message string) {
	t.Helper()
	if !tl.AppendAt(at, message) {
		t.Fatal("append failed")
	}
}
