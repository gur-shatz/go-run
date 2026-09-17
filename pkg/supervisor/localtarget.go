package supervisor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// LocalTargetKind classifies one target.txt reading.
type LocalTargetKind int

const (
	// LocalTargetNone: the file is absent or empty. Run current.txt (or the
	// factory version when nothing usable is on disk).
	LocalTargetNone LocalTargetKind = iota
	// LocalTargetStable: the literal word "stable"; run whatever stable.txt names.
	LocalTargetStable
	// LocalTargetVersion: a concrete version string.
	LocalTargetVersion
	// LocalTargetPointer: "@<name>"; resolve the channel pointer <name> at the
	// effective remote (location.yml or the remote: block) with the same
	// @redirect chain rules remote directed mode uses.
	LocalTargetPointer
)

// LocalTarget is the parsed contents of <component>/target.txt: the version a
// local agent (normally the running child) wants the supervisor to run. Unlike
// forced_versions.txt it never overrides rejects.txt.
type LocalTarget struct {
	Kind LocalTargetKind
	// Version is the concrete version for LocalTargetVersion, or the pointer
	// name (without the leading @) for LocalTargetPointer.
	Version string
}

// String renders the target the way it would be written to target.txt.
func (this LocalTarget) String() string {
	switch this.Kind {
	case LocalTargetStable:
		return "stable"
	case LocalTargetVersion:
		return this.Version
	case LocalTargetPointer:
		return "@" + this.Version
	default:
		return ""
	}
}

// ReadLocalTarget parses target.txt. A missing or empty file yields
// LocalTargetNone and no error. The file holds one line: a version, the word
// "stable", or "@<pointer>". Because the writer is the application rather
// than the operator, the value is checked to be a safe version-folder name
// before it is trusted anywhere near a path.
func ReadLocalTarget(path string) (LocalTarget, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return LocalTarget{}, nil
		}
		return LocalTarget{}, fmt.Errorf("read %s: %w", path, err)
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return LocalTarget{}, nil
	}
	if strings.ContainsAny(body, "\r\n") {
		return LocalTarget{}, fmt.Errorf("%s: expected a single line", path)
	}
	if body == "stable" {
		return LocalTarget{Kind: LocalTargetStable}, nil
	}
	if strings.HasPrefix(body, "@") {
		name := strings.TrimPrefix(body, "@")
		if err := validateVersionName(name); err != nil {
			return LocalTarget{}, fmt.Errorf("%s: pointer %q: %w", path, body, err)
		}
		return LocalTarget{Kind: LocalTargetPointer, Version: name}, nil
	}
	if err := validateVersionName(body); err != nil {
		return LocalTarget{}, fmt.Errorf("%s: version %q: %w", path, body, err)
	}
	return LocalTarget{Kind: LocalTargetVersion, Version: body}, nil
}

// validateVersionName accepts the names the supervisor is willing to use as
// a versions/<name> folder and a URL path segment: non-empty, no path
// separators, not "." or "..", no whitespace or control characters.
func validateVersionName(name string) error {
	if name == "" {
		return errors.New("empty")
	}
	if name == "." || name == ".." {
		return errors.New("not a version name")
	}
	for _, r := range name {
		switch {
		case r == '/' || r == '\\':
			return errors.New("must not contain a path separator")
		case unicode.IsSpace(r) || unicode.IsControl(r):
			return errors.New("must not contain whitespace or control characters")
		}
	}
	return nil
}

// Location is the parsed contents of <component>/location.yml: where locally
// directed mode fetches from. It is operator-owned; the application is not
// expected to write it. The bearer may be given inline, by environment
// variable name, or by file path, in that order of precedence.
type Location struct {
	BaseURL    string `yaml:"base_url"`
	Secret     string `yaml:"secret,omitempty"`
	SecretEnv  string `yaml:"secret_env,omitempty"`
	SecretFile string `yaml:"secret_file,omitempty"`
}

// ReadLocation parses location.yml. The second result is false when the file
// is absent, which the caller treats as "use the remote: block". The returned
// Location has its Secret resolved (secret_env / secret_file already applied).
func ReadLocation(path string) (Location, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Location{}, false, nil
		}
		return Location{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var loc Location
	if err := yaml.Unmarshal(data, &loc); err != nil {
		return Location{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	loc.BaseURL = strings.TrimSpace(loc.BaseURL)
	if loc.BaseURL == "" {
		return Location{}, false, fmt.Errorf("%s: base_url is required", path)
	}
	if err := validateRemoteBaseURL(loc.BaseURL); err != nil {
		return Location{}, false, fmt.Errorf("%s: base_url: %w", path, err)
	}
	secret, err := loc.resolveSecret()
	if err != nil {
		return Location{}, false, fmt.Errorf("%s: %w", path, err)
	}
	loc.Secret = secret
	return loc, true, nil
}

func (this Location) resolveSecret() (string, error) {
	if this.Secret != "" {
		return this.Secret, nil
	}
	if this.SecretEnv != "" {
		return os.Getenv(this.SecretEnv), nil
	}
	if this.SecretFile != "" {
		data, err := os.ReadFile(this.SecretFile)
		if err != nil {
			return "", fmt.Errorf("secret_file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", nil
}

// Apply returns remote with the location's base URL substituted and, when
// the location names one, its bearer. A location without a secret keeps the
// remote: block's bearer (same tenant token, different mirror). Target,
// polling interval and the signing key path are left alone: the key
// deliberately stays under the operator's supervisor.yml so location.yml
// cannot move the trust boundary.
func (this Location) Apply(remote RemoteConfig) RemoteConfig {
	remote.BaseURL = this.BaseURL
	if this.Secret != "" {
		remote.Secret = this.Secret
	}
	return remote
}

// validateRemoteBaseURL accepts what the remote client can fetch: an http(s)
// URL with a host, or an absolute file:// URL.
func validateRemoteBaseURL(raw string) error {
	if strings.HasPrefix(raw, "file://") {
		p := strings.TrimPrefix(strings.TrimPrefix(raw, "file://"), "localhost")
		if !strings.HasPrefix(p, "/") {
			return errors.New("file:// URL must be absolute")
		}
		return nil
	}
	return validateHTTPBaseURL(raw)
}
