// Package logtail provides log-file utilities shared between runctl and the
// supervisor: timestamped rotation, marker writing, and efficient tail /
// paginated reads of large files.
package logtail

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RotateLogFile renames a non-empty log file at path to "<base>.<suffix><ext>".
// Returns nil if the file is missing or empty (nothing to rotate).
func RotateLogFile(path, suffix string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	rotated := fmt.Sprintf("%s.%s%s", base, suffix, ext)
	return os.Rename(path, rotated)
}

// WriteMarker appends a prominent timestamped separator block to the log file
// at path. The block is composed and written in a single call so it stays
// contiguous even if a process is concurrently appending to the same file.
func WriteMarker(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	ts := time.Now().Format("2006-01-02 15:04:05")
	bar := strings.Repeat("=", 80)
	block := fmt.Sprintf("\n%s\n======== MARKER %s ========\n%s\n\n", bar, ts, bar)
	if _, err := f.WriteString(block); err != nil {
		return fmt.Errorf("write marker: %w", err)
	}
	return nil
}

// TailFile reads the last n lines from a file. Returns the lines and any error.
func TailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat log file: %w", err)
	}

	if stat.Size() == 0 {
		return nil, nil
	}

	// For small files, just read all lines.
	if stat.Size() < 1024*1024 { // < 1MB
		return ReadAllLines(f, n)
	}

	return SeekTail(f, stat.Size(), n)
}

// ReadAllLines reads up to n lines from r (most recent ones if there are more).
func ReadAllLines(r io.Reader, n int) ([]string, error) {
	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// ReadLineRange reads lines from offset to offset+limit from a file.
// If limit is 0, no lines are returned (useful for getting just totalLines).
// Returns the lines, total line count in the file, and any error.
func ReadLineRange(path string, offset, limit int) ([]string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line length

	var lines []string
	lineNum := 0
	for scanner.Scan() {
		if limit > 0 && lineNum >= offset && lineNum < offset+limit {
			lines = append(lines, scanner.Text())
		}
		lineNum++
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return lines, lineNum, nil
}

// SeekTail returns the last n lines of an open file by reading a chunk from
// the end. Used by TailFile for large files; exposed for callers that have
// already opened the file.
func SeekTail(f *os.File, size int64, n int) ([]string, error) {
	chunkSize := min(int64(256*1024), size)

	buf := make([]byte, chunkSize)
	offset := size - chunkSize
	_, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}

	// Count newlines from end to find the start of the last n lines.
	count := 0
	pos := len(buf) - 1
	for pos >= 0 {
		if buf[pos] == '\n' {
			count++
			if count > n {
				pos++
				break
			}
		}
		pos--
	}
	if pos < 0 {
		pos = 0
	}

	chunk := buf[pos:]
	chunk = bytes.TrimRight(chunk, "\n")
	var lines []string
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		lines = append(lines, string(line))
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}
