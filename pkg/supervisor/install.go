package supervisor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"sync"
)

// Installer performs the download → verify → extract → swap pipeline for a
// single component. One instance can be reused across install attempts.
//
// Remote is the client built from the configured remote: block. When a call
// arrives with a RemoteConfig carrying a different bearer (locally directed
// mode, where location.yml may redirect a component), the installer keeps
// one extra client for that bearer and swaps it when the bearer changes.
type Installer struct {
	Remote    *RemoteClient
	PublicKey ed25519.PublicKey

	altMu     sync.Mutex
	alt       *RemoteClient
	altBearer string
}

// client returns the RemoteClient to use for remote: the configured one when
// remote names no bearer or the same bearer, otherwise a cached client
// carrying remote.Secret.
func (this *Installer) client(remote RemoteConfig) *RemoteClient {
	if this.Remote == nil || remote.Secret == "" || remote.Secret == this.Remote.bearer {
		return this.Remote
	}
	this.altMu.Lock()
	defer this.altMu.Unlock()
	if this.alt == nil || this.altBearer != remote.Secret {
		c := NewRemoteClient(remote.Secret)
		c.SetPlatform(this.Remote.goos, this.Remote.goarch)
		this.alt, this.altBearer = c, remote.Secret
	}
	return this.alt
}

// ResolveVersion resolves a channel pointer at remote using the client that
// matches remote's bearer.
func (this *Installer) ResolveVersion(ctx context.Context, component string, remote RemoteConfig, target string) (string, error) {
	if remote.BaseURL == "" {
		return "", errNoRemote
	}
	return this.client(remote).ResolveVersion(ctx, remote.BaseURL, component, target)
}

// errNoRemote is returned when a version must be fetched but neither the
// remote: block nor location.yml names a base URL.
var errNoRemote = errors.New("no remote configured (remote.base_url and location.yml are both absent)")

// Install resolves the latest version per the remote target, fetches the
// platform-appropriate archive and signature, verifies, extracts into
// versions/<version>/, and atomically swaps current.txt to point at it.
//
// Returns the resolved version. If err != nil and errors.Is(err, ErrAlreadyCurrent),
// the version on the remote matches current.txt already and nothing changed.
// The caller decides whether that warrants a relaunch.
func (this *Installer) Install(ctx context.Context, component string, remote RemoteConfig, paths ComponentPaths) (string, error) {
	version, err := this.ResolveVersion(ctx, component, remote, remote.Target)
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

	if remote.BaseURL == "" {
		return fmt.Errorf("download %s: %w", version, errNoRemote)
	}
	client := this.client(remote)
	archive, err := client.FetchArchive(ctx, remote.BaseURL, component, version)
	if err != nil {
		return fmt.Errorf("download %s: %w", version, err)
	}
	// Signature verification is skipped when no public key is configured.
	// Intended for file:// remotes in trusted dev environments; production
	// HTTP remotes should always set remote.signature_public_key_path.
	if this.PublicKey != nil {
		sig, err := client.FetchSignature(ctx, remote.BaseURL, component, version)
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
