package supervisor

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ProcessLog duplicates the supervisor process stdout/stderr into a combined
// rotating file while preserving the original stdout/stderr destinations for container logs,
// terminals, or systemd journals.
type ProcessLog struct {
	RunID     string
	Dir       string
	Path      string
	PID       int
	StartedAt time.Time

	stdout *streamTee
	stderr *streamTee
	log    *rotatingFile
}

type ProcessLogStatus struct {
	RunID     string `json:"run_id"`
	PID       int    `json:"pid"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
	Message   string `json:"message,omitempty"`
}

var processConsole = struct {
	mu     sync.RWMutex
	stdout *os.File
	stderr *os.File
}{
	stdout: os.Stdout,
	stderr: os.Stderr,
}

func processConsoleStdout() *os.File {
	processConsole.mu.RLock()
	defer processConsole.mu.RUnlock()
	return processConsole.stdout
}

func processConsoleStderr() *os.File {
	processConsole.mu.RLock()
	defer processConsole.mu.RUnlock()
	return processConsole.stderr
}

func setProcessConsoleStreams(stdout, stderr *os.File) {
	processConsole.mu.Lock()
	defer processConsole.mu.Unlock()
	processConsole.stdout = stdout
	processConsole.stderr = stderr
}

// StartProcessLog creates a new combined per-run supervisor log stream and tees
// the current process stdout/stderr into it.
func StartProcessLog(paths Paths, maxSize int64, maxFiles int) (*ProcessLog, error) {
	if err := markStaleProcessLogs(paths); err != nil {
		return nil, err
	}

	startedAt := time.Now().UTC()
	pid := os.Getpid()
	runID := startedAt.Format("20060102-150405Z") + fmt.Sprintf("-pid%d", pid)
	dir := paths.SupervisorLogs()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create supervisor log dir %s: %w", dir, err)
	}

	logPath := paths.SupervisorRunLog(runID)
	logFile, err := openRotatingFile(logPath, maxSize, maxFiles)
	if err != nil {
		return nil, err
	}
	stdout, err := startStreamTee(int(os.Stdout.Fd()), "stdout", logFile)
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}
	stderr, err := startStreamTee(int(os.Stderr.Fd()), "stderr", logFile)
	if err != nil {
		_ = stdout.Close()
		_ = logFile.Close()
		return nil, err
	}
	setProcessConsoleStreams(stdout.original, stderr.original)

	pl := &ProcessLog{RunID: runID, Dir: dir, Path: logPath, PID: pid, StartedAt: startedAt, stdout: stdout, stderr: stderr, log: logFile}
	if err := pl.Mark("running", ""); err != nil {
		_ = pl.Close()
		return nil, err
	}
	return pl, nil
}

// Mark records the current lifecycle status for this supervisor process run.
func (this *ProcessLog) Mark(status, message string) error {
	if this == nil {
		return nil
	}
	return writeProcessLogStatus(filepath.Join(this.Dir, this.RunID+"_status.json"), ProcessLogStatus{
		RunID:     this.RunID,
		PID:       this.PID,
		Status:    status,
		StartedAt: this.StartedAt.Format(time.RFC3339),
		EndedAt:   endedAtForStatus(status),
		Message:   message,
	})
}

// Close restores stdout/stderr and closes the log files. It is best-effort so
// shutdown logging does not mask the supervisor's real exit reason.
func (this *ProcessLog) Close() error {
	if this == nil {
		return nil
	}
	var first error
	if this.stdout != nil {
		if err := this.stdout.Close(); err != nil && first == nil {
			first = err
		}
	}
	if this.stderr != nil {
		if err := this.stderr.Close(); err != nil && first == nil {
			first = err
		}
	}
	if this.log != nil {
		if err := this.log.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func markStaleProcessLogs(paths Paths) error {
	root := paths.SupervisorLogs()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read supervisor log dir %s: %w", root, err)
	}
	for _, entry := range entries {
		for _, statusPath := range processStatusPaths(root, entry) {
			data, err := os.ReadFile(statusPath)
			if err != nil {
				continue
			}
			var status ProcessLogStatus
			if err := json.Unmarshal(data, &status); err != nil {
				continue
			}
			if status.Status != "running" || status.EndedAt != "" {
				continue
			}
			status.Status = "crashed"
			status.EndedAt = time.Now().UTC().Format(time.RFC3339)
			status.Message = "supervisor did not record a clean exit before the next start"
			if err := writeProcessLogStatus(statusPath, status); err != nil {
				return err
			}
		}
	}
	return nil
}

func processStatusPaths(root string, entry fs.DirEntry) []string {
	name := entry.Name()
	if entry.IsDir() {
		return []string{filepath.Join(root, name, "status.json")}
	}
	if strings.HasSuffix(name, "_status.json") {
		return []string{filepath.Join(root, name)}
	}
	return nil
}

func endedAtForStatus(status string) string {
	if status == "running" {
		return ""
	}
	return time.Now().UTC().Format(time.RFC3339)
}

func writeProcessLogStatus(path string, status ProcessLogStatus) error {
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	return nil
}

type streamTee struct {
	fd       int
	original *os.File
	reader   *os.File
	log      io.Writer
	done     chan error
}

func startStreamTee(fd int, label string, logWriter io.Writer) (*streamTee, error) {
	origFD, err := syscall.Dup(fd)
	if err != nil {
		return nil, fmt.Errorf("dup %s: %w", label, err)
	}
	original := os.NewFile(uintptr(origFD), label+"-original")

	reader, writer, err := os.Pipe()
	if err != nil {
		_ = original.Close()
		return nil, fmt.Errorf("pipe %s: %w", label, err)
	}

	if err := dupFD(int(writer.Fd()), fd); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = original.Close()
		return nil, fmt.Errorf("redirect %s: %w", label, err)
	}
	_ = writer.Close()

	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.MultiWriter(original, logWriter), reader)
		done <- err
	}()

	return &streamTee{
		fd:       fd,
		original: original,
		reader:   reader,
		log:      logWriter,
		done:     done,
	}, nil
}

func (this *streamTee) Close() error {
	if this == nil {
		return nil
	}
	var first error
	if err := dupFD(int(this.original.Fd()), this.fd); err != nil && first == nil {
		first = fmt.Errorf("restore fd %d: %w", this.fd, err)
	}
	if err := <-this.done; err != nil && first == nil {
		first = err
	}
	if err := this.reader.Close(); err != nil && first == nil {
		first = err
	}
	if err := this.original.Close(); err != nil && first == nil {
		first = err
	}
	return first
}
