package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

// MaxRedirectChain is the maximum number of @redirect indirections the poller
// will follow before giving up. A loop or overflow is treated as a polling
// failure (no version is rejected).
const MaxRedirectChain = 5

// ErrRedirectLoop is returned when a required.txt chain exceeds MaxRedirectChain
// or visits the same URL twice.
var ErrRedirectLoop = errors.New("required.txt redirect loop or overflow")

// ErrImageNotFoundForPlatform is returned when the resolved version's archive
// or signature is missing for this supervisor's GOOS/GOARCH. This is a
// polling failure, not a rejection — the vendor simply has not published yet.
var ErrImageNotFoundForPlatform = errors.New("image not published for this platform")

// ErrNoPublishedVersion is returned when the origin's version pointer
// (required.txt) itself is missing: the origin answered, but nothing is
// published there yet (or the URL is wrong). Distinct from
// ErrImageNotFoundForPlatform, which is about a resolved version lacking an
// archive for this GOOS/GOARCH.
var ErrNoPublishedVersion = errors.New("origin has no published version yet (version pointer not found)")

// errNotFound marks a 404 / missing file at the transport layer. Callers
// translate it into the message that fits what was being fetched — the
// version pointer (ErrNoPublishedVersion) or a platform image
// (ErrImageNotFoundForPlatform).
var errNotFound = errors.New("not found")

// RemoteClient wraps an *http.Client with the timeouts and helpers needed to
// poll a vendor update endpoint. The zero value is not usable; call NewRemoteClient.
type RemoteClient struct {
	http   *http.Client
	bearer string

	goos   string
	goarch string
}

// NewRemoteClient returns a RemoteClient with sensible timeouts. bearer, if
// non-empty, is sent as Authorization: Bearer <bearer> on every request.
func NewRemoteClient(bearer string) *RemoteClient {
	return &RemoteClient{
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
		bearer: bearer,
		goos:   runtime.GOOS,
		goarch: runtime.GOARCH,
	}
}

// SetPlatform overrides the GOOS/GOARCH used to build image URLs. Useful for
// tests that want to verify cross-platform URL construction.
func (this *RemoteClient) SetPlatform(goos, goarch string) {
	this.goos = goos
	this.goarch = goarch
}

// ResolveVersion fetches base + "/" + component + "/versions/" + target,
// following @redirect entries up to MaxRedirectChain. Returns the version
// string with whitespace trimmed.
func (this *RemoteClient) ResolveVersion(ctx context.Context, base, component, target string) (string, error) {
	startURL, err := joinURL(base, component, "versions", target)
	if err != nil {
		return "", err
	}

	current := startURL
	visited := map[string]bool{current: true}

	for hop := 0; hop <= MaxRedirectChain; hop++ {
		body, err := this.fetchText(ctx, current)
		if err != nil {
			if errors.Is(err, errNotFound) {
				return "", fmt.Errorf("%s: %w", current, ErrNoPublishedVersion)
			}
			return "", err
		}
		body = strings.TrimSpace(body)

		if !strings.HasPrefix(body, "@") {
			if body == "" {
				return "", fmt.Errorf("%s: empty body", current)
			}
			return body, nil
		}

		next := strings.TrimSpace(body[1:])
		if next == "" {
			return "", fmt.Errorf("%s: empty redirect target", current)
		}
		nextURL, err := redirectTo(current, next)
		if err != nil {
			return "", err
		}
		if visited[nextURL] {
			return "", ErrRedirectLoop
		}
		visited[nextURL] = true
		current = nextURL
	}
	return "", ErrRedirectLoop
}

// ImageURLs returns the (archive, signature) URLs for a resolved version and
// the supervisor's platform.
func (this *RemoteClient) ImageURLs(base, component, version string) (string, string, error) {
	suffix := fmt.Sprintf("%s_%s_%s.tar.gz", version, this.goos, this.goarch)
	archiveURL, err := joinURL(base, component, "images", suffix)
	if err != nil {
		return "", "", err
	}
	sigURL := archiveURL + ".sig"
	return archiveURL, sigURL, nil
}

// FetchArchive fetches the platform-appropriate archive only. The install
// pipeline uses this directly when signature verification is disabled.
func (this *RemoteClient) FetchArchive(ctx context.Context, base, component, version string) ([]byte, error) {
	archiveURL, _, err := this.ImageURLs(base, component, version)
	if err != nil {
		return nil, err
	}
	return this.fetchImageBytes(ctx, archiveURL)
}

// FetchSignature fetches just the detached .sig.
func (this *RemoteClient) FetchSignature(ctx context.Context, base, component, version string) ([]byte, error) {
	_, sigURL, err := this.ImageURLs(base, component, version)
	if err != nil {
		return nil, err
	}
	return this.fetchImageBytes(ctx, sigURL)
}

// fetchImageBytes fetches an archive or signature URL, reporting a 404 /
// missing file as ErrImageNotFoundForPlatform.
func (this *RemoteClient) fetchImageBytes(ctx context.Context, raw string) ([]byte, error) {
	data, err := this.fetchBytes(ctx, raw)
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("%s: %w", raw, ErrImageNotFoundForPlatform)
	}
	return data, err
}

// DownloadImage fetches the platform-appropriate archive and its .sig in one
// call. A 404 on either is reported as ErrImageNotFoundForPlatform.
func (this *RemoteClient) DownloadImage(ctx context.Context, base, component, version string) (archive, sig []byte, err error) {
	archive, err = this.FetchArchive(ctx, base, component, version)
	if err != nil {
		return nil, nil, err
	}
	sig, err = this.FetchSignature(ctx, base, component, version)
	if err != nil {
		return nil, nil, err
	}
	return archive, sig, nil
}

func (this *RemoteClient) fetchText(ctx context.Context, raw string) (string, error) {
	body, err := this.fetchBytes(ctx, raw)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (this *RemoteClient) fetchBytes(ctx context.Context, raw string) ([]byte, error) {
	// file:// — read directly off disk. No auth, no conditional GET, no retries.
	// A missing file is reported the same way as a 404 over HTTP so the install
	// pipeline can treat both as "vendor has not published for this platform yet."
	if strings.HasPrefix(raw, "file://") {
		return readFileURL(raw)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", raw, err)
	}
	if this.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+this.bearer)
	}

	resp, err := this.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", raw, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// fallthrough
	case http.StatusNotFound:
		return nil, fmt.Errorf("%s: %w", raw, errNotFound)
	default:
		return nil, fmt.Errorf("GET %s: %s", raw, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", raw, err)
	}
	return body, nil
}

// readFileURL parses a file:// URL and returns the file's bytes. ENOENT is
// converted to errNotFound so callers can branch the same way they do for
// HTTP 404.
func readFileURL(raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", raw, err)
	}
	if u.Host != "" && u.Host != "localhost" {
		return nil, fmt.Errorf("file:// host must be empty or localhost: %s", raw)
	}
	data, err := os.ReadFile(u.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", raw, errNotFound)
		}
		return nil, fmt.Errorf("read %s: %w", u.Path, err)
	}
	return data, nil
}

// joinURL appends path segments to base with one slash, URL-escaping each
// segment. base must be an absolute URL.
func joinURL(base string, segments ...string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base %s: %w", base, err)
	}
	parts := append([]string{strings.TrimSuffix(u.Path, "/")}, segments...)
	u.Path = strings.Join(parts, "/")
	return u.String(), nil
}

// redirectTo resolves a redirect target against the current URL. A bare name
// (no slashes, no scheme) replaces the last path segment; an absolute URL
// replaces everything.
func redirectTo(current, target string) (string, error) {
	cu, err := url.Parse(current)
	if err != nil {
		return "", err
	}
	tu, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("parse redirect %q: %w", target, err)
	}
	if tu.IsAbs() {
		return tu.String(), nil
	}
	resolved := cu.ResolveReference(tu)
	return resolved.String(), nil
}
