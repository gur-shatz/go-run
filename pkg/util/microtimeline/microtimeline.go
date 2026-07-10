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
type Entry struct {
	Offset     time.Duration `json:"offset" yaml:"offset"`
	OffsetRaw  uint16        `json:"offset_raw" yaml:"offset_raw"`
	OffsetUnit string        `json:"offset_unit" yaml:"offset_unit"`
	Message    string        `json:"message" yaml:"message"`
	Truncated  bool          `json:"truncated,omitempty" yaml:"truncated,omitempty"`
	Clamped    bool          `json:"clamped,omitempty" yaml:"clamped,omitempty"`
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

// WriteYAML writes a YAML array containing the retained timeline entries.
func (this *Timeline) WriteYAML(w io.Writer) error {
	entries, err := this.Snapshot()
	if err != nil {
		return err
	}
	enc := yaml.NewEncoder(w)
	defer enc.Close()
	return enc.Encode(entries)
}

// WriteText writes one retained event per line.
func (this *Timeline) WriteText(w io.Writer) error {
	entries, err := this.Snapshot()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		marks := ""
		if entry.Truncated || entry.Clamped {
			var parts []string
			if entry.Truncated {
				parts = append(parts, "truncated")
			}
			if entry.Clamped {
				parts = append(parts, "clamped")
			}
			marks = " [" + strings.Join(parts, ",") + "]"
		}
		if _, err := fmt.Fprintf(w, "+%s %s%s\n", entry.Offset, entry.Message, marks); err != nil {
			return err
		}
	}
	return nil
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
