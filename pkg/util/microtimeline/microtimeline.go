package microtimeline

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	headerLen = 6
	magic     = 0x9d

	flagUnitMask   = 0x03
	flagClamped    = 1 << 2
	flagTruncated  = 1 << 3
	flagWrapMarker = 1 << 4

	unitNS = 0
	unitUS = 1
	unitMS = 2
	unitS  = 3
)

var (
	ErrBufferTooSmall = errors.New("microtimeline buffer too small")
	ErrBusy           = errors.New("microtimeline busy")
)

// Timeline is a byte-constrained cyclic event buffer.
//
// Each event is stored in a compact binary record:
//
//	uint16 len | uint8 flags | uint8 magic | uint16 offset | message bytes
//
// The offset is relative to the previous appended event and uses the smallest
// time unit that can represent it: nanoseconds, microseconds, milliseconds, or
// seconds. All methods are safe for concurrent use.
type Timeline struct {
	mu sync.Mutex

	buf  []byte
	head int
	tail int
	used int

	last time.Time
}

// Entry is a decoded timeline event.
//
// At is the reconstructed absolute event time: the timeline anchors its
// newest event at the last append's wall-clock time and walks the stored
// offsets backwards. Offsets are quantized to their storage unit (ns / us
// / ms / s at uint16 range), so reconstructed times drift by up to one
// unit per hop away from the newest event; a Clamped entry means its
// offset overflowed even the seconds unit and times at or before it are
// unreliable.
type Entry struct {
	At         time.Time     `json:"at" yaml:"at"`
	Offset     time.Duration `json:"offset" yaml:"offset"`
	OffsetRaw  uint16        `json:"offset_raw" yaml:"offset_raw"`
	OffsetUnit string        `json:"offset_unit" yaml:"offset_unit"`
	Message    string        `json:"message" yaml:"message"`
	Truncated  bool          `json:"truncated,omitempty" yaml:"truncated,omitempty"`
	Clamped    bool          `json:"clamped,omitempty" yaml:"clamped,omitempty"`
}

// Render formats the entry as one human line relative to now:
//
//	20060102-15:04:05 (23s ago) message [truncated,clamped]
func (this Entry) Render(now time.Time) string {
	return fmt.Sprintf("%s (%s ago) %s%s", this.At.Format("20060102-15:04:05"), this.ago(now), this.Message, this.marks())
}

// ago returns the display-rounded distance from now to the event.
func (this Entry) ago(now time.Time) time.Duration {
	ago := now.Sub(this.At)
	if ago >= time.Second {
		return ago.Round(time.Second)
	}
	return ago.Round(time.Millisecond)
}

// marks renders the entry's storage flags as a " [truncated,clamped]"
// suffix, or "" when clean.
func (this Entry) marks() string {
	if !this.Truncated && !this.Clamped {
		return ""
	}
	var parts []string
	if this.Truncated {
		parts = append(parts, "truncated")
	}
	if this.Clamped {
		parts = append(parts, "clamped")
	}
	return " [" + strings.Join(parts, ",") + "]"
}

// New returns a Timeline backed by one fixed-size byte buffer.
func New(bufferSize int) (*Timeline, error) {
	if bufferSize < headerLen+1 {
		return nil, ErrBufferTooSmall
	}
	return &Timeline{buf: make([]byte, bufferSize)}, nil
}

// Append records message in the timeline without blocking. It returns false if
// another goroutine is using the timeline.
func (this *Timeline) Append(message string) bool {
	return this.AppendAt(time.Now(), message)
}

// Appendln records a message formatted like fmt.Println without the trailing
// newline. It returns false if another goroutine is using the timeline.
func (this *Timeline) Appendln(a ...any) bool {
	return this.Append(strings.TrimSuffix(fmt.Sprintln(a...), "\n"))
}

// Appendf records a message formatted like fmt.Printf. It returns false if
// another goroutine is using the timeline.
func (this *Timeline) Appendf(format string, a ...any) bool {
	return this.Append(fmt.Sprintf(format, a...))
}

// AppendAt records message with an explicit event time without blocking. It is
// mostly useful in tests and for callers that already sampled the event
// timestamp.
func (this *Timeline) AppendAt(at time.Time, message string) bool {
	if this == nil || len(this.buf) < headerLen+1 {
		return false
	}

	if !this.mu.TryLock() {
		return false
	}
	defer this.mu.Unlock()

	delta := time.Duration(0)
	if !this.last.IsZero() {
		delta = at.Sub(this.last)
		if delta < 0 {
			delta = 0
		}
	}
	this.last = at

	flags, raw := encodeOffset(delta)
	msg := []byte(message)
	maxMsg := len(this.buf) - headerLen
	if len(msg) > maxMsg {
		msg = msg[:maxMsg]
		flags |= flagTruncated
	}

	recLen := headerLen + len(msg)
	this.ensureWritable(recLen)
	this.writeRecord(recLen, flags, raw, msg)
	return true
}

// Snapshot decodes the currently retained events from oldest to newest without
// blocking. It returns ErrBusy if another goroutine is using the timeline.
func (this *Timeline) Snapshot() ([]Entry, error) {
	if this == nil {
		return nil, nil
	}

	if !this.mu.TryLock() {
		return nil, ErrBusy
	}
	defer this.mu.Unlock()

	out := make([]Entry, 0)
	pos := this.tail
	remaining := this.used
	for remaining > 0 {
		if pos >= len(this.buf) {
			pos = 0
		}
		if remaining < headerLen {
			break
		}
		if pos+headerLen > len(this.buf) {
			consumed := len(this.buf) - pos
			if consumed > remaining {
				break
			}
			remaining -= consumed
			pos = 0
			continue
		}
		if isZeroRun(this.buf[pos:]) {
			consumed := len(this.buf) - pos
			if consumed > remaining {
				break
			}
			remaining -= consumed
			pos = 0
			continue
		}

		recLen := int(binary.LittleEndian.Uint16(this.buf[pos:]))
		flags := this.buf[pos+2]
		if recLen == 0 || this.buf[pos+3] != magic || flags&flagWrapMarker != 0 {
			consumed := len(this.buf) - pos
			if consumed > remaining {
				break
			}
			remaining -= consumed
			pos = 0
			continue
		}
		if recLen < headerLen || recLen > remaining || pos+recLen > len(this.buf) {
			break
		}

		raw := binary.LittleEndian.Uint16(this.buf[pos+4:])
		msg := string(this.buf[pos+headerLen : pos+recLen])
		out = append(out, Entry{
			Offset:     decodeOffset(flags, raw),
			OffsetRaw:  raw,
			OffsetUnit: unitName(flags),
			Message:    msg,
			Truncated:  flags&flagTruncated != 0,
			Clamped:    flags&flagClamped != 0,
		})

		pos += recLen
		remaining -= recLen
	}

	// Reconstruct absolute event times: the newest entry happened at the
	// last append's wall time; each earlier entry precedes its successor
	// by the successor's stored offset.
	at := this.last
	for i := len(out) - 1; i >= 0; i-- {
		out[i].At = at
		at = at.Add(-out[i].Offset)
	}
	return out, nil
}

// WriteJSON writes a JSON array containing the retained timeline entries.
func (this *Timeline) WriteJSON(w io.Writer) error {
	entries, err := this.Snapshot()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

// WriteYAML writes a YAML sequence with one rendered line per retained
// event ("20060102-15:04:05 (23s ago) message") — valid YAML that still
// reads like a log. Structured access goes through Snapshot / WriteJSON.
func (this *Timeline) WriteYAML(w io.Writer) error {
	entries, err := this.Snapshot()
	if err != nil {
		return err
	}
	enc := yaml.NewEncoder(w)
	defer enc.Close()
	return enc.Encode(RenderEntries(entries, time.Now()))
}

// WriteText writes one rendered event per line, same shape as the YAML
// sequence items.
func (this *Timeline) WriteText(w io.Writer) error {
	entries, err := this.Snapshot()
	if err != nil {
		return err
	}
	for _, line := range RenderEntries(entries, time.Now()) {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// RenderEntries renders entries into the one-line-per-event form relative
// to now, for callers serializing a filtered or captured snapshot.
func RenderEntries(entries []Entry, now time.Time) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Render(now)
	}
	return out
}

// WriteMarkdown writes the retained events as a GFM table (Time | Ago |
// Event), the shape the chiutil backoffice viewer renders for .md routes.
func (this *Timeline) WriteMarkdown(w io.Writer) error {
	entries, err := this.Snapshot()
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, RenderEntriesMarkdown(entries, time.Now()))
	return err
}

// RenderEntriesMarkdown renders entries as a GFM table relative to now.
// Messages are wrapped in code spans so markdown-active characters in
// event text (underscores, asterisks, brackets) render verbatim.
func RenderEntriesMarkdown(entries []Entry, now time.Time) string {
	var sb strings.Builder
	sb.WriteString("| Time | Ago | Event |\n| --- | --- | --- |\n")
	for _, entry := range entries {
		fmt.Fprintf(&sb, "| %s | %s | %s%s |\n",
			entry.At.Format("20060102-15:04:05"), entry.ago(now), markdownCell(entry.Message), entry.marks())
	}
	return sb.String()
}

// markdownCell renders one message as a table-safe code span: pipes are
// escaped (they end table cells even inside code spans), newlines become
// spaces (a cell is one line), and messages containing backticks get a
// double-backtick delimiter per the GFM code-span rules.
func markdownCell(message string) string {
	if message == "" {
		return ""
	}
	message = strings.ReplaceAll(message, "\n", " ")
	message = strings.ReplaceAll(message, "|", `\|`)
	if strings.Contains(message, "`") {
		return "`` " + message + " ``"
	}
	return "`" + message + "`"
}

func (this *Timeline) ensureWritable(recLen int) {
	endSpace := len(this.buf) - this.head
	padding := 0
	if endSpace < recLen {
		padding = endSpace
	}

	for len(this.buf)-this.used < recLen+padding {
		this.dropOne()
	}

	if padding > 0 {
		this.writeWrapMarker(padding)
		this.head = 0
		this.used += padding
	}
}

func (this *Timeline) writeRecord(recLen int, flags byte, raw uint16, msg []byte) {
	binary.LittleEndian.PutUint16(this.buf[this.head:], uint16(recLen))
	this.buf[this.head+2] = flags
	this.buf[this.head+3] = magic
	binary.LittleEndian.PutUint16(this.buf[this.head+4:], raw)
	copy(this.buf[this.head+headerLen:this.head+recLen], msg)

	this.head += recLen
	this.used += recLen
	if this.head == len(this.buf) {
		this.head = 0
	}
	if this.used == recLen {
		this.tail = this.head - recLen
		if this.tail < 0 {
			this.tail += len(this.buf)
		}
	}
}

func (this *Timeline) writeWrapMarker(n int) {
	if n >= headerLen {
		binary.LittleEndian.PutUint16(this.buf[this.head:], uint16(n))
		this.buf[this.head+2] = flagWrapMarker
		this.buf[this.head+3] = magic
		binary.LittleEndian.PutUint16(this.buf[this.head+4:], 0)
		for i := this.head + headerLen; i < len(this.buf); i++ {
			this.buf[i] = 0
		}
		return
	}
	for i := this.head; i < len(this.buf); i++ {
		this.buf[i] = 0
	}
}

func (this *Timeline) dropOne() {
	if this.used == 0 {
		return
	}
	if this.tail >= len(this.buf) {
		this.tail = 0
	}
	if this.tail+headerLen > len(this.buf) || isZeroRun(this.buf[this.tail:]) {
		consumed := len(this.buf) - this.tail
		this.tail = 0
		this.used -= min(consumed, this.used)
		return
	}

	recLen := int(binary.LittleEndian.Uint16(this.buf[this.tail:]))
	flags := this.buf[this.tail+2]
	if recLen < headerLen || this.buf[this.tail+3] != magic || flags&flagWrapMarker != 0 {
		consumed := len(this.buf) - this.tail
		this.tail = 0
		this.used -= min(consumed, this.used)
		return
	}
	if recLen > this.used || this.tail+recLen > len(this.buf) {
		this.tail = this.head
		this.used = 0
		return
	}

	this.tail += recLen
	this.used -= recLen
	if this.tail == len(this.buf) {
		this.tail = 0
	}
}

func encodeOffset(d time.Duration) (byte, uint16) {
	if d <= time.Duration(math.MaxUint16) {
		return unitNS, uint16(d)
	}
	if d/time.Microsecond <= math.MaxUint16 {
		return unitUS, uint16(d / time.Microsecond)
	}
	if d/time.Millisecond <= math.MaxUint16 {
		return unitMS, uint16(d / time.Millisecond)
	}
	if d/time.Second <= math.MaxUint16 {
		return unitS, uint16(d / time.Second)
	}
	return flagClamped | unitS, math.MaxUint16
}

func decodeOffset(flags byte, raw uint16) time.Duration {
	switch flags & flagUnitMask {
	case unitUS:
		return time.Duration(raw) * time.Microsecond
	case unitMS:
		return time.Duration(raw) * time.Millisecond
	case unitS:
		return time.Duration(raw) * time.Second
	default:
		return time.Duration(raw)
	}
}

func unitName(flags byte) string {
	switch flags & flagUnitMask {
	case unitUS:
		return "us"
	case unitMS:
		return "ms"
	case unitS:
		return "s"
	default:
		return "ns"
	}
}

func isZeroRun(buf []byte) bool {
	for _, b := range buf {
		if b != 0 {
			return false
		}
	}
	return len(buf) > 0
}
