package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// rotatingFile is an io.WriteCloser that writes to a single log file with
// size-capped rotation. When a Write would cross the cap the active file is
// renamed to "<name>.1", any existing .1 → .2 etc. up to historyN; older
// generations are deleted.
//
// The writer is safe for concurrent Write/Close calls. It is intentionally
// simple — no time-based rotation, no compression. Operators who want
// richer behaviour run an external rotator (logrotate) against the per-
// component log directory.
type rotatingFile struct {
	path     string
	maxSize  int64
	maxFiles int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// openRotatingFile creates the parent directory if needed, opens path for
// append, and seeds the in-memory size from the file's current length.
func openRotatingFile(path string, maxSize int64, maxFiles int) (*rotatingFile, error) {
	if maxSize <= 0 {
		return nil, fmt.Errorf("rotatingFile: maxSize must be > 0 (got %d)", maxSize)
	}
	if maxFiles < 0 {
		return nil, fmt.Errorf("rotatingFile: maxFiles must be >= 0 (got %d)", maxFiles)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	return &rotatingFile{
		path:     path,
		maxSize:  maxSize,
		maxFiles: maxFiles,
		f:        f,
		size:     info.Size(),
	}, nil
}

// Write appends p to the active file. If the resulting size would exceed
// maxSize the file is rotated first.
func (this *rotatingFile) Write(p []byte) (int, error) {
	this.mu.Lock()
	defer this.mu.Unlock()

	if this.size+int64(len(p)) > this.maxSize {
		if err := this.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := this.f.Write(p)
	this.size += int64(n)
	return n, err
}

// Close releases the underlying file. Subsequent Writes will fail.
func (this *rotatingFile) Close() error {
	this.mu.Lock()
	defer this.mu.Unlock()
	if this.f == nil {
		return nil
	}
	err := this.f.Close()
	this.f = nil
	return err
}

// rotateLocked must be called with mu held. It closes the active file,
// shifts existing generations, opens a fresh active file, and resets size.
func (this *rotatingFile) rotateLocked() error {
	if err := this.f.Close(); err != nil {
		return fmt.Errorf("close %s before rotate: %w", this.path, err)
	}
	// Drop the oldest generation (path.<maxFiles>) if present.
	if this.maxFiles > 0 {
		oldest := this.path + "." + strconv.Itoa(this.maxFiles)
		_ = os.Remove(oldest)
	}
	// Shift each generation up by one (path.N → path.N+1) starting from
	// the highest existing one to avoid overwriting.
	for i := this.maxFiles - 1; i >= 1; i-- {
		src := this.path + "." + strconv.Itoa(i)
		dst := this.path + "." + strconv.Itoa(i+1)
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("rotate %s → %s: %w", src, dst, err)
			}
		}
	}
	// Active file becomes .1 (only if we keep history).
	if this.maxFiles >= 1 {
		dst := this.path + ".1"
		if err := os.Rename(this.path, dst); err != nil {
			return fmt.Errorf("rotate %s → %s: %w", this.path, dst, err)
		}
	} else {
		// History disabled: just discard the active file.
		_ = os.Remove(this.path)
	}
	f, err := os.OpenFile(this.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", this.path, err)
	}
	this.f = f
	this.size = 0
	return nil
}

