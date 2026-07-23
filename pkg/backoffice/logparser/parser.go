package logparser

import (
	"bytes"
	"regexp"
	"strings"
	"time"
)

var (
	ansiEscapeRE    = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	escapedColorRE  = regexp.MustCompile(`\[[0-9;]+m`)
	keyStartRE      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*=`)
	levelNormalizeR = strings.NewReplacer("[", "", "]", "")
)

// TimestampedParser parses common timestamped structured console logs.
type TimestampedParser struct{}

// ConsoleParser is kept as a compatibility alias for the previous parser name.
type ConsoleParser = TimestampedParser

// StartsEntryLine reports whether a physical line starts a timestamped log entry.
func (TimestampedParser) StartsEntryLine(line []byte) bool {
	field := firstConsoleField(line)
	if !looksLikeConsoleTime(field) {
		return false
	}
	_, ok := parseConsoleTime(string(field))
	return ok
}

// IgnoreLine drops visual separators that should not become standalone entries
// or continuation text.
func (TimestampedParser) IgnoreLine(line []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(line), []byte("===="))
}

// ParseLine parses one already-decoded log line.
func (TimestampedParser) ParseLine(line []byte, meta LineMeta) (Entry, bool) {
	clean := strings.TrimRight(stripANSIBytes(line), "\r\n")
	entry := Entry{
		Stream:     meta.Stream,
		Segment:    meta.Segment,
		Line:       meta.Line,
		Offset:     meta.Offset,
		NextOffset: meta.NextOffset,
		Raw:        clean,
		Message:    clean,
		Truncated:  meta.Truncated,
	}

	header := clean
	if newline := strings.IndexByte(header, '\n'); newline >= 0 {
		header = header[:newline]
	}
	fields := strings.Fields(header)
	if len(fields) < 5 {
		return entry, true
	}
	ts, ok := parseConsoleTime(fields[0])
	if !ok {
		return entry, true
	}

	entry.Time = &ts
	entry.Level = levelNormalizeR.Replace(fields[1])
	entry.Logger = fields[2]
	entry.Caller = fields[3]

	rest := strings.Join(fields[4:], " ")
	message, kv := splitMessageFields(rest)
	entry.Message = message
	entry.Fields = kv
	return entry, true
}

// Match applies the generic filter behavior.
func (TimestampedParser) Match(entry Entry, filter Filter) bool {
	return MatchEntry(entry, filter)
}

// NaiveParser treats each physical line as a standalone entry.
type NaiveParser struct{}

// ParseLine parses one already-decoded log line without timestamp or field extraction.
func (NaiveParser) ParseLine(line []byte, meta LineMeta) (Entry, bool) {
	clean := strings.TrimRight(stripANSIBytes(line), "\r\n")
	return Entry{
		Stream:     meta.Stream,
		Segment:    meta.Segment,
		Line:       meta.Line,
		Offset:     meta.Offset,
		NextOffset: meta.NextOffset,
		Raw:        clean,
		Message:    clean,
		Truncated:  meta.Truncated,
	}, true
}

// Match applies the generic filter behavior.
func (NaiveParser) Match(entry Entry, filter Filter) bool {
	return MatchEntry(entry, filter)
}

func parseConsoleTime(value string) (time.Time, bool) {
	layout := time.RFC3339Nano
	if n := len(value); n >= 5 && (value[n-5] == '+' || value[n-5] == '-') && value[n-3] != ':' {
		layout = "2006-01-02T15:04:05.999999999-0700"
	}
	parsed, err := time.Parse(layout, value)
	return parsed, err == nil
}

func firstConsoleField(line []byte) []byte {
	start := 0
	for start < len(line) && isASCIISpace(line[start]) {
		start++
	}
	end := start
	for end < len(line) && !isASCIISpace(line[end]) {
		end++
	}
	return line[start:end]
}

func looksLikeConsoleTime(value []byte) bool {
	if len(value) < len("2006-01-02T15:04:05Z") {
		return false
	}
	for _, i := range [...]int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return value[4] == '-' && value[7] == '-' && value[10] == 'T' && value[13] == ':' && value[16] == ':'
}

func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func stripANSIBytes(line []byte) string {
	line = ansiEscapeRE.ReplaceAll(line, nil)
	line = escapedColorRE.ReplaceAll(line, nil)
	return string(line)
}

func splitMessageFields(rest string) (string, map[string]string) {
	tokens := splitShellish(rest)
	firstKV := len(tokens)
	for firstKV > 0 && keyStartRE.MatchString(tokens[firstKV-1]) {
		firstKV--
	}
	if firstKV == len(tokens) {
		return rest, nil
	}

	fields := map[string]string{}
	for _, token := range tokens[firstKV:] {
		key, value, ok := strings.Cut(token, "=")
		if !ok || key == "" {
			continue
		}
		fields[key] = strings.Trim(value, `"`)
	}
	if len(fields) == 0 {
		return rest, nil
	}
	return strings.Join(tokens[:firstKV], " "), fields
}

func splitShellish(s string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			inQuote = !inQuote
			b.WriteRune(r)
		case (r == ' ' || r == '\t') && !inQuote:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// MatchEntry applies the generic filter behavior.
func MatchEntry(entry Entry, filter Filter) bool {
	if len(filter.Level) > 0 && !containsFold(filter.Level, entry.Level) {
		return false
	}
	if len(filter.Logger) > 0 && !containsFold(filter.Logger, entry.Logger) {
		return false
	}
	if filter.Since != nil {
		if entry.Time == nil || entry.Time.Before(*filter.Since) {
			return false
		}
	}
	if filter.Until != nil {
		if entry.Time == nil || entry.Time.After(*filter.Until) {
			return false
		}
	}
	for key, want := range filter.Fields {
		if entry.Fields == nil || entry.Fields[key] != want {
			return false
		}
	}
	if filter.Text != "" {
		haystack := entry.Raw + "\n" + entry.Message
		needle := filter.Text
		if filter.CaseFold {
			haystack = strings.ToLower(haystack)
			needle = strings.ToLower(needle)
		}
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	if filter.Regex != "" {
		re := filter.regex
		if re == nil || re.String() != filter.Regex {
			var err error
			re, err = regexp.Compile(filter.Regex)
			if err != nil {
				return false
			}
		}
		if !re.MatchString(entry.Raw) && !re.MatchString(entry.Message) {
			return false
		}
	}
	return true
}

// PrepareFilter precompiles regular expressions for repeated matching.
func PrepareFilter(filter Filter) (Filter, error) {
	if filter.Regex == "" {
		filter.regex = nil
		return filter, nil
	}
	if filter.regex != nil && filter.regex.String() == filter.Regex {
		return filter, nil
	}
	re, err := regexp.Compile(filter.Regex)
	if err != nil {
		return Filter{}, err
	}
	filter.regex = re
	return filter, nil
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
