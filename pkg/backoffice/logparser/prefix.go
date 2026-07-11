package logparser

import "strings"

// PrefixParser dispatches parsing by stream-name prefix, falling back to Default.
type PrefixParser struct {
	Default  Parser
	Prefixes map[string]Parser
}

func (p PrefixParser) ParseLine(line []byte, meta LineMeta) (Entry, bool) {
	return p.parserFor(meta.Stream, meta.Segment).ParseLine(line, meta)
}

func (p PrefixParser) Match(entry Entry, filter Filter) bool {
	return p.parserFor(entry.Stream, entry.Segment).Match(entry, filter)
}

func (p PrefixParser) StartsEntryLineForStream(stream string, line []byte) bool {
	parser := p.parserFor(stream, "")
	if classifier, ok := parser.(LineClassifier); ok {
		return classifier.StartsEntryLine(line)
	}
	return true
}

func (p PrefixParser) IgnoreLineForStream(stream string, line []byte) bool {
	parser := p.parserFor(stream, "")
	if classifier, ok := parser.(LineClassifier); ok {
		return classifier.IgnoreLine(line)
	}
	return false
}

func (p PrefixParser) parserFor(values ...string) Parser {
	var bestPrefix string
	var best Parser
	for prefix, parser := range p.Prefixes {
		if parser == nil || !hasAnyPrefix(values, prefix) {
			continue
		}
		if len(prefix) > len(bestPrefix) {
			bestPrefix = prefix
			best = parser
		}
	}
	if best != nil {
		return best
	}
	if p.Default != nil {
		return p.Default
	}
	return NaiveParser{}
}

func hasAnyPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
