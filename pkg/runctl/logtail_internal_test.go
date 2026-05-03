package runctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	if err := os.WriteFile(path, []byte("before\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeMarker(path); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	if !strings.HasPrefix(s, "before\n") {
		t.Errorf("existing content was not preserved: %q", s)
	}
	if !strings.Contains(s, "======== MARKER ") {
		t.Errorf("marker label missing: %q", s)
	}
	bar := strings.Repeat("=", 80)
	if strings.Count(s, bar) < 2 {
		t.Errorf("expected at least two %d-char separator bars, got %q", len(bar), s)
	}
	if !strings.HasSuffix(s, "\n\n") {
		t.Errorf("expected trailing blank line, got %q", s)
	}
}

func TestWriteMarkerCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.log")

	if err := writeMarker(path); err != nil {
		t.Fatalf("writeMarker: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "======== MARKER ") {
		t.Errorf("marker label missing in newly created file: %q", string(data))
	}
}

func TestRotateLogFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.run.log")
	if err := os.WriteFile(path, []byte("old content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := rotateLogFile(path, "20260101-000000"); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("original file should be gone, stat err = %v", err)
	}
	rotated := filepath.Join(dir, "hello.run.20260101-000000.log")
	data, err := os.ReadFile(rotated)
	if err != nil {
		t.Fatalf("rotated file unreadable: %v", err)
	}
	if string(data) != "old content\n" {
		t.Errorf("rotated content mismatch: %q", string(data))
	}
}

func TestRotateLogFileSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.log")
	if err := rotateLogFile(path, "20260101-000000"); err != nil {
		t.Errorf("missing file should be a no-op, got: %v", err)
	}
}

func TestRotateLogFileSkipsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := rotateLogFile(path, "20260101-000000"); err != nil {
		t.Fatalf("rotate empty: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("empty file should still exist, got: %v", err)
	}
	rotated := filepath.Join(dir, "empty.20260101-000000.log")
	if _, err := os.Stat(rotated); !os.IsNotExist(err) {
		t.Errorf("empty file should not be rotated, stat err = %v", err)
	}
}
