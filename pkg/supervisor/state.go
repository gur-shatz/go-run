package supervisor

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// atomicWrite writes data to path via the WriteFile → fsync → Rename pattern.
// The parent directory is created with 0755 if it does not exist. The temp file
// shares the destination directory so Rename is a same-filesystem atomic swap.
func atomicWrite(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s → %s: %w", tmpPath, path, err)
	}
	return nil
}

// readVersionFile returns the single version string in a stable/current-style
// file, trimmed of trailing whitespace. Returns ("", nil) if the file does not
// exist; the caller decides whether absence is an error.
func readVersionFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimRight(string(data), "\r\n \t"), nil
}

// writeVersionFile atomically writes a single newline-terminated version string.
// An empty string clears the file (still newline-terminated for shell friendliness).
func writeVersionFile(path, version string) error {
	return atomicWrite(path, []byte(version+"\n"), 0644)
}

// ReadStable returns the contents of stable.txt for a component, or "" if absent.
func (this ComponentPaths) ReadStable() (string, error) {
	return readVersionFile(this.Stable())
}

// WriteStable atomically replaces stable.txt with the given version string.
func (this ComponentPaths) WriteStable(version string) error {
	return writeVersionFile(this.Stable(), version)
}

// ReadCurrent returns the contents of current.txt for a component, or "" if absent.
func (this ComponentPaths) ReadCurrent() (string, error) {
	return readVersionFile(this.Current())
}

// WriteCurrent atomically replaces current.txt. This is the commit point of an install.
func (this ComponentPaths) WriteCurrent(version string) error {
	return writeVersionFile(this.Current(), version)
}

// RejectEntry is one row of rejects.txt: the rejected version plus the time
// the supervisor recorded the rejection. A zero RejectedAt means a legacy
// bare-version entry from before timestamps were introduced — treated as
// "rejected indefinitely" so we never accidentally un-reject historical bans.
type RejectEntry struct {
	Version    string
	RejectedAt time.Time
}

// ReadRejectEntries parses every line of rejects.txt into a RejectEntry. The
// on-disk format is "<version> <RFC3339-timestamp>" per line. A bare version
// (no timestamp) is preserved with RejectedAt = time.Time{}. Returns an empty
// slice if the file does not exist.
func (this ComponentPaths) ReadRejectEntries() ([]RejectEntry, error) {
	f, err := os.Open(this.Rejects())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", this.Rejects(), err)
	}
	defer f.Close()

	var out []RejectEntry
	seen := make(map[string]bool)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		version, ts := parseRejectLine(line)
		if version == "" || seen[version] {
			continue
		}
		seen[version] = true
		out = append(out, RejectEntry{Version: version, RejectedAt: ts})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", this.Rejects(), err)
	}
	return out, nil
}

// parseRejectLine splits a line into (version, timestamp). Tolerates lines
// without a timestamp (returns zero time) so legacy files keep working.
func parseRejectLine(line string) (string, time.Time) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", time.Time{}
	}
	if len(fields) == 1 {
		return fields[0], time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, fields[1])
	if err != nil {
		return fields[0], time.Time{}
	}
	return fields[0], ts
}

// ReadRejects returns the version names from rejects.txt, ignoring timestamps.
// Useful for orphan-folder GC that cares about "ever rejected" rather than
// "currently active rejection". For the active-rejection check use
// IsActivelyRejected.
func (this ComponentPaths) ReadRejects() ([]string, error) {
	entries, err := this.ReadRejectEntries()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Version)
	}
	return out, nil
}

// IsRejected reports whether version appears in rejects.txt at all. This is
// the "ever rejected" check; for the supervisor's decision logic use
// IsActivelyRejected so expired rejections autonomously clear.
func (this ComponentPaths) IsRejected(version string) (bool, error) {
	rejects, err := this.ReadRejects()
	if err != nil {
		return false, err
	}
	return slices.Contains(rejects, version), nil
}

// IsActivelyRejected reports whether version is currently considered rejected
// given the configured expiry: it must appear in rejects.txt AND the
// recorded timestamp must be within the last `expiry`. A zero `expiry`
// disables autonomous clearing — every entry stays active.
//
// The autonomous-clear contract: if `expiry` has passed since the rejection
// timestamp, the next decide() will not skip the version. If it really is
// bad it'll crash and earn a fresh rejection on the next bad-version cycle.
func (this ComponentPaths) IsActivelyRejected(version string, now time.Time, expiry time.Duration) (bool, error) {
	entries, err := this.ReadRejectEntries()
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Version != version {
			continue
		}
		// Legacy entry without a timestamp: indefinite (safe default).
		if e.RejectedAt.IsZero() {
			return true, nil
		}
		if expiry <= 0 {
			return true, nil
		}
		return now.Sub(e.RejectedAt) < expiry, nil
	}
	return false, nil
}

// AppendReject atomically rewrites rejects.txt with the given version stamped
// at the current time. Appending the same version twice replaces the old
// timestamp so the expiry window restarts — a crashing version that keeps
// trying gets back-to-back fresh stamps.
func (this ComponentPaths) AppendReject(version string) error {
	existing, err := this.ReadRejectEntries()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	replaced := false
	for i := range existing {
		if existing[i].Version == version {
			existing[i].RejectedAt = now
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, RejectEntry{Version: version, RejectedAt: now})
	}

	var buf strings.Builder
	for _, e := range existing {
		buf.WriteString(e.Version)
		if !e.RejectedAt.IsZero() {
			buf.WriteByte(' ')
			buf.WriteString(e.RejectedAt.Format(time.RFC3339))
		}
		buf.WriteByte('\n')
	}
	return atomicWrite(this.Rejects(), []byte(buf.String()), 0644)
}

// EnsureDirs creates the component's root and versions directories if missing.
func (this ComponentPaths) EnsureDirs() error {
	if err := os.MkdirAll(this.Root, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", this.Root, err)
	}
	if err := os.MkdirAll(this.Versions(), 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", this.Versions(), err)
	}
	return nil
}
