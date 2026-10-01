package supervisor

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// authFile is what the supervisor persists under <state_dir>/auth.yml for
// credentials set up through the UI rather than the config file. The config
// file is authoritative: a field it provides shadows the same field here.
// Passwords are stored as a salted PBKDF2-SHA256 hash, never in clear.
type authFile struct {
	Username     string `yaml:"username,omitempty"`
	PasswordHash string `yaml:"password_hash,omitempty"`
	TOTPSecret   string `yaml:"totp_secret,omitempty"`
}

// authStore reads and writes the auth file atomically with owner-only
// permissions.
type authStore struct {
	path string
}

func newAuthStore(path string) *authStore {
	return &authStore{path: path}
}

// load returns the persisted file, or a zero value when none exists yet.
func (this *authStore) load() (authFile, error) {
	var f authFile
	b, err := os.ReadFile(this.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", this.path, err)
	}
	return f, nil
}

// save writes the file via a temp sibling + rename so a crash mid-write can't
// leave a truncated auth file behind.
func (this *authStore) save(f authFile) error {
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(this.path), 0755); err != nil {
		return err
	}
	tmp := this.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, this.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Password hashing: "pbkdf2-sha256$<iterations>$<salt b64>$<key b64>". The
// scheme is spelled out in the string so it can be migrated later without
// guessing what an old value is.
const (
	pwHashScheme = "pbkdf2-sha256"
	pwHashIter   = 600_000 // OWASP 2023 guidance for PBKDF2-HMAC-SHA256
	pwHashLen    = 32
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pwHashIter, pwHashLen)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding.EncodeToString
	return strings.Join([]string{pwHashScheme, strconv.Itoa(pwHashIter), enc(salt), enc(key)}, "$"), nil
}

// verifyPassword reports whether password matches a hashPassword output. A
// malformed hash never matches.
func verifyPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != pwHashScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	dec := base64.RawStdEncoding.DecodeString
	salt, err := dec(parts[2])
	if err != nil {
		return false
	}
	want, err := dec(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}
