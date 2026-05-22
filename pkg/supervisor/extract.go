package supervisor

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxArchiveEntries caps the number of entries one ExtractTarGz call will
// process before returning an error. A safety belt against pathological
// archives.
const MaxArchiveEntries = 100_000

// ExtractTarGz streams the gzip-compressed tarball in r into destDir,
// rejecting any entry whose path is absolute or contains "..". Mode bits are
// preserved within the 0777 mask; ownership is not (children all run as the
// supervisor's UID for v0).
//
// On any error the partially extracted destDir is removed by the caller —
// ExtractTarGz does not clean up after itself so the caller can inspect on
// failure if needed.
func ExtractTarGz(r io.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	gzr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		entries++
		if entries > MaxArchiveEntries {
			return fmt.Errorf("archive exceeds %d entries", MaxArchiveEntries)
		}

		if err := checkArchivePath(hdr.Name); err != nil {
			return err
		}
		target := filepath.Join(destDir, filepath.FromSlash(hdr.Name))

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, fileMode(hdr.Mode, 0755)); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			if err := writeRegular(tr, target, fileMode(hdr.Mode, 0644)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := checkArchivePath(hdr.Linkname); err != nil {
				return fmt.Errorf("symlink %s: %w", hdr.Name, err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("symlink %s -> %s: %w", target, hdr.Linkname, err)
			}
		default:
			// Skip unsupported types (block/char devices, fifos, hard links).
			// Tar archives shipped by a build pipeline shouldn't carry these.
			continue
		}
	}
}

// checkArchivePath rejects absolute paths, Windows-drive prefixes, and any
// component equal to "..". This is the tar-slip guard.
func checkArchivePath(name string) error {
	if name == "" {
		return fmt.Errorf("archive entry has empty name")
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return fmt.Errorf("archive entry %q is absolute", name)
	}
	cleaned := filepath.ToSlash(filepath.Clean(name))
	for part := range strings.SplitSeq(cleaned, "/") {
		if part == ".." {
			return fmt.Errorf("archive entry %q escapes destination", name)
		}
	}
	return nil
}

func writeRegular(r io.Reader, target string, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", target, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", target, err)
	}
	return f.Close()
}

func fileMode(headerMode int64, fallback os.FileMode) os.FileMode {
	if headerMode == 0 {
		return fallback
	}
	return os.FileMode(headerMode) & 0777
}
