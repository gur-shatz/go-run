package logparser

import (
	"strings"
	"testing"
)

func TestTimestampedParser(t *testing.T) {
	line := []byte(`2026-07-10T10:00:06.310Z    [34mINFO[0m    boot          gateway/gateway_main.go:558    Gateway front-door request method=POST host=gateway.firegatenetworks.com route="/mcp" status=401 user_agent="Claude-User"`)
	entry, ok := (TimestampedParser{}).ParseLine(line, LineMeta{Stream: "gateway.log", Segment: "gateway.log", Offset: 5, NextOffset: 10})
	if !ok {
		t.Fatal("expected parser to keep line")
	}
	if entry.Level != "INFO" {
		t.Fatalf("level = %q, want INFO", entry.Level)
	}
	if entry.Logger != "boot" {
		t.Fatalf("logger = %q, want boot", entry.Logger)
	}
	if entry.Message != "Gateway front-door request" {
		t.Fatalf("message = %q", entry.Message)
	}
	if entry.Fields["route"] != "/mcp" || entry.Fields["status"] != "401" || entry.Fields["user_agent"] != "Claude-User" {
		t.Fatalf("fields = %#v", entry.Fields)
	}
	if strings.Contains(entry.Raw, "[34m") || strings.Contains(entry.Raw, "[0m") {
		t.Fatalf("raw still contains color markers: %q", entry.Raw)
	}
}

func TestTimestampedParserPreservesKeyValuePairsInsideMessage(t *testing.T) {
	const message = `Issuer: outbound token exchange failed: account="5PfymfNeaZJ" system="ap5Q4yHxoqffu": rpc error: code = Unavailable desc = Post "https://mcp-bitbucket.sapiens.com/token": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`
	line := []byte(`2026-07-10T10:00:06.310Z ERROR issuer issuer.go:123 ` + message)

	entry, ok := (TimestampedParser{}).ParseLine(line, LineMeta{})
	if !ok {
		t.Fatal("expected parser to keep line")
	}
	if entry.Message != message {
		t.Fatalf("message = %q, want %q", entry.Message, message)
	}
	if entry.Fields != nil {
		t.Fatalf("fields = %#v, want none", entry.Fields)
	}
}

func TestNaiveParser(t *testing.T) {
	entry, ok := (NaiveParser{}).ParseLine([]byte("plain log line\n"), LineMeta{Stream: "app.log", Offset: 3, NextOffset: 18})
	if !ok {
		t.Fatal("expected parser to keep line")
	}
	if entry.Raw != "plain log line" || entry.Message != "plain log line" {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.Time != nil || entry.Level != "" || entry.Logger != "" || entry.Caller != "" {
		t.Fatalf("naive parser extracted structured fields: %#v", entry)
	}
}

func TestPrepareFilterReusesCompiledRegex(t *testing.T) {
	filter, err := PrepareFilter(Filter{Regex: `status=(?:200|500)`})
	if err != nil {
		t.Fatal(err)
	}
	if filter.regex == nil {
		t.Fatal("regex was not compiled")
	}
	compiled := filter.regex
	filter, err = PrepareFilter(filter)
	if err != nil {
		t.Fatal(err)
	}
	if filter.regex != compiled {
		t.Fatal("prepared regex was compiled again")
	}
	if !MatchEntry(Entry{Raw: "request status=500"}, filter) {
		t.Fatal("prepared regex did not match")
	}
}
