package supervisor

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrAlreadyRunning is returned by AcquireLock when another supervisor process
// already holds the lock on state_dir/supervisor.lock.
var ErrAlreadyRunning = errors.New("another supervisor is already running against this state_dir")

// FileLock holds an exclusive flock on a path. The kernel releases the lock
// when the process dies, so a crashed supervisor never leaves a stale lock.
type FileLock struct {
	path string
	file *os.File
}

// AcquireLock tries to take an exclusive non-blocking flock on path. The file
// is created with 0644 if missing. ErrAlreadyRunning is returned when another
// process already holds the lock.
func AcquireLock(path string) (*FileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}

	return &FileLock{path: path, file: f}, nil
}

// Release drops the lock and closes the underlying file. Safe to call on a nil
// receiver.
func (this *FileLock) Release() error {
	if this == nil || this.file == nil {
		return nil
	}
	// Best-effort explicit unlock; closing the fd would release it anyway.
	_ = syscall.Flock(int(this.file.Fd()), syscall.LOCK_UN)
	err := this.file.Close()
	this.file = nil
	return err
}
