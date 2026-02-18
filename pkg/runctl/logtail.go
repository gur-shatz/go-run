package runctl

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
)

// tailFile reads the last n lines from a file. Returns the lines and any error.
func tailFile(path string, n int) ([]string, error) {
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

	// For small files, just read all lines
	if stat.Size() < 1024*1024 { // < 1MB
		return readAllLines(f, n)
	}

	// For large files, seek from end
	return seekTail(f, stat.Size(), n)
}

func readAllLines(r io.Reader, n int) ([]string, error) {
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

// readLineRange reads lines from offset to offset+limit from a file.
// If limit is 0, no lines are returned (useful for getting just totalLines).
// Returns the lines, total line count in the file, and any error.
func readLineRange(path string, offset, limit int) ([]string, int, error) {
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

func seekTail(f *os.File, size int64, n int) ([]string, error) {
	chunkSize := min(int64(256*1024), size)

	buf := make([]byte, chunkSize)
	offset := size - chunkSize
	_, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}

	// Count newlines from end to find the start of the last n lines
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

	// Split the relevant portion into lines
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
