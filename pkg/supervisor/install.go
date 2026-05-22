package supervisor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
)

// Installer performs the download → verify → extract → swap pipeline for a
// single component. It is stateless; one instance can be reused across
// install attempts.
type Installer struct {
	Remote    *RemoteClient
	PublicKey ed25519.PublicKey
}

// Install resolves the latest version per the remote target, fetches the
// platform-appropriate archive and signature, verifies, extracts into
// versions/<version>/, and atomically swaps current.txt to point at it.
//
// Returns the resolved version. If err != nil and errors.Is(err, ErrAlreadyCurrent),
// the version on the remote matches current.txt already and nothing changed.
// The caller decides whether that warrants a relaunch.
func (this *Installer) Install(ctx context.Context, component string, remote RemoteConfig, paths ComponentPaths) (string, error) {
	version, err := this.Remote.ResolveVersion(ctx, remote.BaseURL, component, remote.Target)
	if err != nil {
		return "", fmt.Errorf("resolve version: %w", err)
	}
	return version, this.InstallVersion(ctx, component, remote, paths, version)
}

// ErrAlreadyCurrent is returned by InstallVersion when the requested version
// already matches current.txt and is fully extracted on disk.
var ErrAlreadyCurrent = errors.New("requested version is already current")

// ErrVersionRejected is returned by InstallVersion when the requested version
// is in rejects.txt. The pipeline does not check forced_versions.txt — the
// caller decides whether an override applies.
var ErrVersionRejected = errors.New("requested version is in rejects.txt")

// InstallVersion materialises a specific version. It is the public form used
// when the version was already known (e.g. from forced_versions.txt) and the
// caller doesn't want to round-trip ResolveVersion.
func (this *Installer) InstallVersion(ctx context.Context, component string, remote RemoteConfig, paths ComponentPaths, version string) error {
	if version == "" {
		return fmt.Errorf("InstallVersion: empty version")
	}
	current, _ := paths.ReadCurrent()
	if current == version && versionExtracted(paths.VersionDir(version)) {
		return ErrAlreadyCurrent
	}
	if err := this.PrepareVersion(ctx, component, remote, paths, version); err != nil {
		return err
	}
	// Atomic commit point: stamp current.txt with the new version.
	if err := paths.WriteCurrent(version); err != nil {
		return fmt.Errorf("write current.txt: %w", err)
	}
	return nil
}

// PrepareVersion downloads, verifies, and extracts version into versions/<v>/
// WITHOUT touching current.txt. A separate step (SwitchToVersion on the
// Component) commits the swap so the supervisor can keep an old child
// running while a new version is fetched in the background.
//
// Idempotent: if versions/<v>/ already contains an extracted archive the
// download is skipped. Returns ErrVersionRejected if version is in rejects.txt
// — only forced overrides should ever ask to install a rejected version.
func (this *Installer) PrepareVersion(ctx context.Context, component string, remote RemoteConfig, paths ComponentPaths, version string) error {
	if version == "" {
		return fmt.Errorf("PrepareVersion: empty version")
	}
	rejected, err := paths.IsRejected(version)
	if err != nil {
		return err
	}
	if rejected {
		return ErrVersionRejected
	}

	versionDir := paths.VersionDir(version)
	if versionExtracted(versionDir) {
		return nil
	}

	archive, err := this.Remote.FetchArchive(ctx, remote.BaseURL, component, version)
	if err != nil {
		return fmt.Errorf("download %s: %w", version, err)
	}
	// Signature verification is skipped when no public key is configured.
	// Intended for file:// remotes in trusted dev environments; production
	// HTTP remotes should always set remote.signature_public_key_path.
	if this.PublicKey != nil {
		sig, err := this.Remote.FetchSignature(ctx, remote.BaseURL, component, version)
		if err != nil {
			return fmt.Errorf("download signature %s: %w", version, err)
		}
		if err := VerifyArchive(archive, sig, this.PublicKey); err != nil {
			return fmt.Errorf("verify %s: %w", version, err)
		}
	}
	// Clean any partial leftover from a previous attempt.
	_ = os.RemoveAll(versionDir)
	if err := ExtractTarGz(bytes.NewReader(archive), versionDir); err != nil {
		_ = os.RemoveAll(versionDir)
		return fmt.Errorf("extract %s: %w", version, err)
	}
	return nil
}

// versionExtracted is a cheap heuristic: the folder exists and contains at
// least one entry. The supervisor never modifies a version folder after
// extraction, so existence is a reliable proxy for "fully unpacked".
func versionExtracted(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

