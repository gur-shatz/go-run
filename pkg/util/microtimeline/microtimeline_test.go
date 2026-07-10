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
	if !strings.Contains(yamlOut.String(), "message: world") {
		t.Fatalf("yaml output = %s", yamlOut.String())
	}

	var textOut bytes.Buffer
	if err := tl.WriteText(&textOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOut.String(), "+1ms world") {
		t.Fatalf("text output = %s", textOut.String())
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
