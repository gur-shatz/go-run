package logviewer

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
)

func (v *Viewer) scan(stream Stream, q Query) (Page, error) {
	filter, err := prepareFilter(q.Filter)
	if err != nil {
		return Page{}, errInvalidFilter
	}
	q.Filter = filter
	if len(stream.Segments) == 0 {
		return Page{EOF: true, BOF: true, Range: rangeForPosition(stream, 0, 0)}, nil
	}
	if q.Direction == "" {
		q.Direction = Forward
	}
	if q.Limit <= 0 {
		q.Limit = v.opts.DefaultLimit
	}
	if q.Limit > v.opts.MaxLimit {
		q.Limit = v.opts.MaxLimit
	}
	if q.Cursor.StreamID == "" {
		q.Cursor.StreamID = stream.ID
	}
	if q.Cursor.StreamID != stream.ID {
		return Page{}, errInvalidCursor
	}
	if q.Cursor.SegmentIndex < 0 || q.Cursor.SegmentIndex >= len(stream.Segments) {
		return Page{}, errInvalidCursor
	}
	if q.Direction == Backward {
		return v.scanBackward(stream, q)
	}
	return v.scanForward(stream, q)
}

func (v *Viewer) tail(stream Stream, limit int) (Page, error) {
	return v.tailWithFilter(stream, limit, Filter{})
}

func (v *Viewer) tailWithFilter(stream Stream, limit int, filter Filter) (Page, error) {
	if len(stream.Segments) == 0 {
		return Page{EOF: true, BOF: true, Range: rangeForPosition(stream, 0, 0)}, nil
	}
	last := len(stream.Segments) - 1
	return v.scan(stream, Query{
		Cursor: Cursor{
			StreamID:     stream.ID,
			SegmentIndex: last,
			Offset:       stream.Segments[last].Size,
		},
		Limit:     limit,
		Direction: Backward,
		Filter:    filter,
	})
}

func (v *Viewer) scanForward(stream Stream, q Query) (Page, error) {
	if q.Filter.Before > 0 || q.Filter.After > 0 {
		return v.scanForwardWithContext(stream, q)
	}
	var entries []Entry
	segIndex := q.Cursor.SegmentIndex
	offset := q.Cursor.Offset
	eof := false
	for segIndex < len(stream.Segments) && len(entries) < q.Limit {
		seg := stream.Segments[segIndex]
		if offset > seg.Size {
			eof = true
			break
		}
		scanned, nextOffset, err := v.scanSegmentForward(stream, seg, offset, q.Limit-len(entries), q.Filter)
		if err != nil {
			return Page{}, err
		}
		entries = append(entries, scanned...)
		if len(entries) >= q.Limit {
			offset = nextOffset
			break
		}
		segIndex++
		offset = 0
	}
	if segIndex >= len(stream.Segments) {
		eof = true
		segIndex = len(stream.Segments) - 1
		offset = stream.Segments[segIndex].Size
	}
	return makePage(entries, stream, eof, false, segIndex, offset), nil
}

func (v *Viewer) scanForwardWithContext(stream Stream, q Query) (Page, error) {
	var entries []Entry
	seen := map[string]int{}
	lookbehind := make([]Entry, 0, q.Filter.Before)
	afterRemaining := 0
	segIndex := q.Cursor.SegmentIndex
	offset := q.Cursor.Offset
	eof := false
	for segIndex < len(stream.Segments) && len(entries) < q.Limit {
		seg := stream.Segments[segIndex]
		if offset > seg.Size {
			eof = true
			break
		}
		nextOffset, reachedEOF, err := v.scanSegmentForwardEntries(stream, seg, offset, func(entry Entry) bool {
			matches := v.parser.Match(entry, q.Filter)
			if matches {
				for i, prior := range lookbehind {
					if len(entries) >= q.Limit {
						return false
					}
					if i == 0 {
						prior.ContextTop = true
					}
					appendUnique(&entries, seen, prior)
				}
				entry.Match = true
				appendUnique(&entries, seen, entry)
				afterRemaining = q.Filter.After
				lookbehind = lookbehind[:0]
				return len(entries) < q.Limit
			}
			if afterRemaining > 0 {
				if afterRemaining == 1 {
					entry.ContextBot = true
				}
				appendUnique(&entries, seen, entry)
				afterRemaining--
				if q.Filter.Before > 0 {
					lookbehind = append(lookbehind, entry)
					if len(lookbehind) > q.Filter.Before {
						copy(lookbehind, lookbehind[1:])
						lookbehind = lookbehind[:q.Filter.Before]
					}
				}
				return len(entries) < q.Limit
			}
			if q.Filter.Before > 0 {
				lookbehind = append(lookbehind, entry)
				if len(lookbehind) > q.Filter.Before {
					copy(lookbehind, lookbehind[1:])
					lookbehind = lookbehind[:q.Filter.Before]
				}
			}
			return true
		})
		if err != nil {
			return Page{}, err
		}
		offset = nextOffset
		if len(entries) >= q.Limit {
			break
		}
		if reachedEOF {
			segIndex++
			offset = 0
			continue
		}
		break
	}
	if segIndex >= len(stream.Segments) {
		eof = true
		segIndex = len(stream.Segments) - 1
		offset = stream.Segments[segIndex].Size
	}
	return makePage(entries, stream, eof, false, segIndex, offset), nil
}

func (v *Viewer) scanSegmentForward(stream Stream, seg Segment, offset int64, limit int, filter Filter) ([]Entry, int64, error) {
	var entries []Entry
	pos, _, err := v.scanSegmentForwardEntries(stream, seg, offset, func(entry Entry) bool {
		if v.parser.Match(entry, filter) {
			entry.Match = filter.Text != "" || filter.Regex != "" || len(filter.Level) > 0 || len(filter.Logger) > 0 || filter.Since != nil || filter.Until != nil || len(filter.Fields) > 0
			entries = append(entries, entry)
		}
		return len(entries) < limit
	})
	return entries, pos, err
}

func (v *Viewer) scanSegmentForwardEntries(stream Stream, seg Segment, offset int64, yield func(Entry) bool) (int64, bool, error) {
	full, err := v.segmentPath(seg)
	if err != nil {
		return offset, false, err
	}
	f, err := os.Open(full)
	if err != nil {
		return offset, false, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, false, err
	}

	reader := bufio.NewReaderSize(f, v.opts.BlockBytes)
	classifier := lineClassifierFor(v.parser)
	pos := offset
	var pending []byte
	var pendingMeta LineMeta
	hasPending := false
	for {
		line, actual, truncated, err := readLineBounded(reader, v.opts.MaxLineBytes)
		if len(line) > 0 || actual > 0 {
			next := pos + int64(actual)
			cleanLine, wasTruncated := truncateLine(line, v.opts.MaxLineBytes)
			meta := LineMeta{
				Stream:     stream.ID,
				Segment:    seg.Path,
				Offset:     pos,
				NextOffset: next,
				Truncated:  truncated || wasTruncated,
			}
			if classifier.IgnoreLine(cleanLine) {
				pos = next
			} else if !hasPending {
				if classifier.StartsEntryLine(cleanLine) {
					pending = bytes.Clone(cleanLine)
					pendingMeta = meta
					hasPending = true
				}
				pos = next
			} else if classifier.StartsEntryLine(cleanLine) {
				if entry, ok := v.parser.ParseLine(pending, pendingMeta); ok {
					if !yield(entry) {
						return pendingMeta.NextOffset, false, nil
					}
				}
				pending = bytes.Clone(cleanLine)
				pendingMeta = meta
				pos = next
			} else {
				pending = appendLogicalContinuation(pending, cleanLine, v.opts.MaxLineBytes)
				pendingMeta.NextOffset = next
				pendingMeta.Truncated = pendingMeta.Truncated || truncated || wasTruncated || len(pending) >= v.opts.MaxLineBytes
				pos = next
			}
		}
		if errors.Is(err, io.EOF) {
			if hasPending {
				if entry, ok := v.parser.ParseLine(pending, pendingMeta); ok {
					if !yield(entry) {
						return pendingMeta.NextOffset, false, nil
					}
				}
			}
			return pos, true, nil
		}
		if err != nil {
			return pos, false, err
		}
	}
}

func (v *Viewer) scanBackward(stream Stream, q Query) (Page, error) {
	// Segment scanners naturally encounter entries newest-first. Keep that order
	// while collecting so each match is one append, then reverse the final page.
	var reverseEntries []Entry
	segIndex := q.Cursor.SegmentIndex
	offset := q.Cursor.Offset
	bof := false
	for segIndex >= 0 && len(reverseEntries) < q.Limit {
		seg := stream.Segments[segIndex]
		if offset > seg.Size {
			offset = seg.Size
		}
		scanned, nextOffset, atBOF, err := v.scanSegmentBackward(stream, seg, offset, q.Limit-len(reverseEntries), q.Filter)
		if err != nil {
			return Page{}, err
		}
		reverseEntries = append(reverseEntries, scanned...)
		if len(reverseEntries) >= q.Limit {
			offset = nextOffset
			break
		}
		if !atBOF {
			break
		}
		segIndex--
		if segIndex >= 0 {
			offset = stream.Segments[segIndex].Size
		}
	}
	if segIndex < 0 {
		bof = true
		segIndex = 0
		offset = 0
	}
	slices.Reverse(reverseEntries)
	return makePage(reverseEntries, stream, false, bof, segIndex, offset), nil
}

func (v *Viewer) scanSegmentBackward(stream Stream, seg Segment, offset int64, limit int, filter Filter) ([]Entry, int64, bool, error) {
	full, err := v.segmentPath(seg)
	if err != nil {
		return nil, offset, false, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, offset, false, err
	}
	defer f.Close()

	// Entries and physical lines are encountered newest-first. carry contains the
	// bounded prefix retained for the one physical line crossing a block edge.
	var reverseEntries []Entry
	var carry reverseLineBuffer
	carryNextOffset := offset
	classifier := lineClassifierFor(v.parser)
	var continuation reverseLineBuffer
	continuationNextOffset := int64(0)

	type backwardLine struct {
		data       []byte
		offset     int64
		nextOffset int64
		truncated  bool
	}
	consume := func(line backwardLine) {
		cleanLine, truncated := truncateLine(line.data, v.opts.MaxLineBytes)
		truncated = truncated || line.truncated
		if classifier.IgnoreLine(cleanLine) {
			return
		}
		if !classifier.StartsEntryLine(cleanLine) {
			if continuation.len() == 0 {
				continuation.prepend(cleanLine, v.opts.MaxLineBytes)
				continuationNextOffset = line.nextOffset
			} else {
				continuation.prepend([]byte{'\n'}, v.opts.MaxLineBytes)
				continuation.prepend(cleanLine, v.opts.MaxLineBytes)
			}
			continuation.truncated = continuation.truncated || truncated
			return
		}

		logicalLine := cleanLine
		nextOffset := line.nextOffset
		logicalTruncated := truncated
		if continuation.len() > 0 {
			logicalLine = appendLogicalContinuation(bytes.Clone(cleanLine), continuation.bytes(), v.opts.MaxLineBytes)
			nextOffset = continuationNextOffset
			logicalTruncated = logicalTruncated || continuation.truncated || len(logicalLine) >= v.opts.MaxLineBytes
			continuation.reset()
			continuationNextOffset = 0
		}
		meta := LineMeta{
			Stream:     stream.ID,
			Segment:    seg.Path,
			Offset:     line.offset,
			NextOffset: nextOffset,
			Truncated:  logicalTruncated,
		}
		if entry, ok := v.parser.ParseLine(logicalLine, meta); ok && v.parser.Match(entry, filter) {
			reverseEntries = append(reverseEntries, entry)
		}
	}

	blockSize := int64(v.opts.BlockBytes)
	blockBuf := make([]byte, v.opts.BlockBytes)
	pos := offset
	for pos > 0 && len(reverseEntries) < limit {
		readStart := pos - blockSize
		if readStart < 0 {
			readStart = 0
		}
		size := pos - readStart
		block := blockBuf[:int(size)]
		if _, err := f.ReadAt(block, readStart); err != nil && !errors.Is(err, io.EOF) {
			return nil, pos, false, err
		}

		lastNewline := bytes.LastIndexByte(block, '\n')
		if lastNewline < 0 {
			carry.prepend(block, v.opts.MaxLineBytes)
			if readStart == 0 && carry.len() > 0 {
				consume(backwardLine{data: carry.bytes(), offset: 0, nextOffset: carryNextOffset, truncated: carry.truncated})
				carry.reset()
			}
			pos = readStart
			continue
		}

		lastPart := block[lastNewline+1:]
		if carry.len() == 0 {
			lastLine, truncated := truncateLine(lastPart, v.opts.MaxLineBytes)
			if len(lastLine) > 0 {
				consume(backwardLine{
					data:       lastLine,
					offset:     readStart + int64(lastNewline+1),
					nextOffset: carryNextOffset,
					truncated:  truncated,
				})
			}
		} else {
			carry.prepend(lastPart, v.opts.MaxLineBytes)
			consume(backwardLine{
				data:       carry.bytes(),
				offset:     readStart + int64(lastNewline+1),
				nextOffset: carryNextOffset,
				truncated:  carry.truncated,
			})
		}
		carry.reset()

		firstNewline := lastNewline
		for firstNewline > 0 && len(reverseEntries) < limit {
			previousNewline := bytes.LastIndexByte(block[:firstNewline], '\n')
			if previousNewline < 0 {
				break
			}
			line, lineTruncated := truncateLine(block[previousNewline+1:firstNewline], v.opts.MaxLineBytes)
			if len(line) == 0 {
				firstNewline = previousNewline
				continue
			}
			consume(backwardLine{
				data:       line,
				offset:     readStart + int64(previousNewline+1),
				nextOffset: readStart + int64(firstNewline+1),
				truncated:  lineTruncated,
			})
			firstNewline = previousNewline
		}

		if len(reverseEntries) < limit {
			firstPart := block[:firstNewline]
			if readStart == 0 {
				first, firstTruncated := truncateLine(firstPart, v.opts.MaxLineBytes)
				if len(first) > 0 {
					consume(backwardLine{
						data:       first,
						offset:     0,
						nextOffset: int64(firstNewline + 1),
						truncated:  firstTruncated,
					})
				}
			} else {
				carry.reset()
				carry.prepend(firstPart, v.opts.MaxLineBytes)
				carryNextOffset = readStart + int64(firstNewline+1)
			}
		}
		pos = readStart
	}

	nextOffset := int64(0)
	if len(reverseEntries) > 0 {
		nextOffset = reverseEntries[len(reverseEntries)-1].Offset
	}
	return reverseEntries, nextOffset, pos == 0, nil
}

func makePage(entries []Entry, stream Stream, eof, bof bool, segIndex int, offset int64) Page {
	if entries == nil {
		entries = []Entry{}
	}
	page := Page{Entries: entries, EOF: eof, BOF: bof}
	if len(entries) > 0 {
		first := entries[0]
		last := entries[len(entries)-1]
		firstSeg := segmentIndexByPath(stream, first.Segment)
		lastSeg := segmentIndexByPath(stream, last.Segment)
		page.PrevCursor = encodeCursor(Cursor{StreamID: stream.ID, SegmentIndex: firstSeg, Offset: first.Offset, Line: first.Line})
		page.NextCursor = encodeCursor(Cursor{StreamID: stream.ID, SegmentIndex: lastSeg, Offset: last.NextOffset, Line: last.Line})
		page.Range = Range{
			StreamID:      stream.ID,
			SegmentCount:  len(stream.Segments),
			TotalBytes:    totalStreamBytes(stream),
			EntryCount:    len(entries),
			StartSegment:  firstSeg,
			StartPath:     first.Segment,
			StartOffset:   first.Offset,
			EndSegment:    lastSeg,
			EndPath:       last.Segment,
			EndOffset:     last.NextOffset,
			StartAbsolute: absoluteOffset(stream, firstSeg, first.Offset),
			EndAbsolute:   absoluteOffset(stream, lastSeg, last.NextOffset),
		}
		return page
	}
	page.NextCursor = encodeCursor(Cursor{StreamID: stream.ID, SegmentIndex: segIndex, Offset: offset})
	page.PrevCursor = page.NextCursor
	if bof {
		page.PrevCursor = ""
	}
	if eof {
		page.NextCursor = ""
	}
	page.Range = rangeForPosition(stream, segIndex, offset)
	return page
}

func segmentIndexByPath(stream Stream, path string) int {
	for i, seg := range stream.Segments {
		if seg.Path == path {
			return i
		}
	}
	return 0
}

func rangeForPosition(stream Stream, segIndex int, offset int64) Range {
	if len(stream.Segments) == 0 {
		return Range{StreamID: stream.ID}
	}
	if segIndex < 0 {
		segIndex = 0
	}
	if segIndex >= len(stream.Segments) {
		segIndex = len(stream.Segments) - 1
	}
	seg := stream.Segments[segIndex]
	if offset < 0 {
		offset = 0
	}
	if offset > seg.Size {
		offset = seg.Size
	}
	abs := absoluteOffset(stream, segIndex, offset)
	return Range{
		StreamID:      stream.ID,
		SegmentCount:  len(stream.Segments),
		TotalBytes:    totalStreamBytes(stream),
		StartSegment:  segIndex,
		StartPath:     seg.Path,
		StartOffset:   offset,
		EndSegment:    segIndex,
		EndPath:       seg.Path,
		EndOffset:     offset,
		StartAbsolute: abs,
		EndAbsolute:   abs,
	}
}

func totalStreamBytes(stream Stream) int64 {
	var total int64
	for _, seg := range stream.Segments {
		total += seg.Size
	}
	return total
}

func absoluteOffset(stream Stream, segIndex int, offset int64) int64 {
	var absolute int64
	for i, seg := range stream.Segments {
		if i == segIndex {
			if offset < 0 {
				offset = 0
			}
			if offset > seg.Size {
				offset = seg.Size
			}
			return absolute + offset
		}
		absolute += seg.Size
	}
	return absolute
}

func appendUnique(entries *[]Entry, seen map[string]int, entry Entry) {
	key := entry.Segment + ":" + strconv.FormatInt(entry.Offset, 10)
	if idx, ok := seen[key]; ok {
		mergeEntryFlags(&(*entries)[idx], entry)
		return
	}
	seen[key] = len(*entries)
	*entries = append(*entries, entry)
}

func mergeEntryFlags(existing *Entry, incoming Entry) {
	if incoming.Match {
		existing.Match = true
		existing.ContextTop = false
		existing.ContextBot = false
		return
	}
	if existing.Match {
		return
	}
	if (existing.ContextTop && incoming.ContextBot) || (existing.ContextBot && incoming.ContextTop) ||
		((existing.ContextTop || existing.ContextBot) && !incoming.ContextTop && !incoming.ContextBot) {
		existing.ContextTop = false
		existing.ContextBot = false
		return
	}
	existing.ContextTop = existing.ContextTop || incoming.ContextTop
	existing.ContextBot = existing.ContextBot || incoming.ContextBot
}

type defaultLineClassifier struct{}

func (defaultLineClassifier) StartsEntryLine([]byte) bool { return true }
func (defaultLineClassifier) IgnoreLine([]byte) bool      { return false }

func lineClassifierFor(parser Parser) LineClassifier {
	if classifier, ok := parser.(LineClassifier); ok {
		return classifier
	}
	return defaultLineClassifier{}
}

type reverseLineBuffer struct {
	buf       []byte
	start     int
	size      int
	truncated bool
}

func (b *reverseLineBuffer) len() int {
	return b.size
}

func (b *reverseLineBuffer) reset() {
	b.start = 0
	b.size = 0
	b.truncated = false
}

func (b *reverseLineBuffer) prepend(prefix []byte, max int) {
	if len(prefix) == 0 {
		return
	}
	if max <= 0 {
		existing := b.bytes()
		b.buf = make([]byte, len(prefix)+len(existing))
		copy(b.buf, prefix)
		copy(b.buf[len(prefix):], existing)
		b.start = 0
		b.size = len(b.buf)
		return
	}

	if len(prefix)+b.size > max {
		b.truncated = true
	}
	prefixLen := min(len(prefix), max)
	keepExisting := min(b.size, max-prefixLen)
	b.size = keepExisting
	b.ensureCapacity(prefixLen+keepExisting, max)

	newStart := b.start - prefixLen
	if newStart < 0 {
		newStart += len(b.buf)
	}
	first := min(prefixLen, len(b.buf)-newStart)
	copy(b.buf[newStart:newStart+first], prefix[:first])
	copy(b.buf[:prefixLen-first], prefix[first:prefixLen])
	b.start = newStart
	b.size += prefixLen
}

func (b *reverseLineBuffer) ensureCapacity(required, maxSize int) {
	if len(b.buf) >= required {
		return
	}
	capacity := max(64, len(b.buf)*2)
	if capacity > maxSize {
		capacity = maxSize
	}
	if capacity < required {
		capacity = required
	}
	next := make([]byte, capacity)
	b.copyTo(next)
	b.buf = next
	b.start = 0
}

func (b *reverseLineBuffer) bytes() []byte {
	out := make([]byte, b.size)
	b.copyTo(out)
	return out
}

func (b *reverseLineBuffer) copyTo(dst []byte) {
	if b.size == 0 {
		return
	}
	first := min(b.size, len(b.buf)-b.start)
	copy(dst, b.buf[b.start:b.start+first])
	copy(dst[first:], b.buf[:b.size-first])
}

func appendLogicalContinuation(base, continuation []byte, max int) []byte {
	base = bytes.TrimRight(base, "\r\n")
	continuation = bytes.TrimRight(continuation, "\r\n")
	if len(continuation) == 0 {
		return base
	}
	if len(base) > 0 {
		base = append(base, '\n')
	}
	base = append(base, continuation...)
	if max > 0 && len(base) > max {
		return base[:max]
	}
	return base
}

func prependLogicalContinuation(existing, line []byte, max int) []byte {
	line = bytes.TrimRight(line, "\r\n")
	existing = bytes.TrimRight(existing, "\r\n")
	if len(existing) > 0 {
		line = append(line, '\n')
	}
	line = append(line, existing...)
	if max > 0 && len(line) > max {
		return line[:max]
	}
	return line
}

func readLineBounded(r *bufio.Reader, max int) ([]byte, int, bool, error) {
	var out []byte
	total := 0
	truncated := false
	for {
		part, err := r.ReadSlice('\n')
		total += len(part)
		if max <= 0 || len(out)+len(part) <= max {
			out = append(out, part...)
		} else {
			keep := max - len(out)
			if keep > 0 {
				out = append(out, part[:keep]...)
			}
			truncated = true
		}
		if err == nil || errors.Is(err, io.EOF) {
			return out, total, truncated, err
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return out, total, truncated, err
	}
}
