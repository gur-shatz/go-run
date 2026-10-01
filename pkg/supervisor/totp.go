package supervisor

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238) with the parameters every authenticator app defaults to:
// HMAC-SHA1, 30-second steps, 6 digits. Implemented on the standard library
// so the login gate stays dependency-free.

const (
	totpStep   = 30 * time.Second
	totpDigits = 6
	// totpSkew is how many steps either side of "now" are accepted, to absorb
	// clock drift between the server and the operator's phone.
	totpSkew = 1
)

// totpEncoding is unpadded base32, the form authenticator apps and
// otpauth:// URIs use for the shared secret.
var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// decodeTOTPSecret parses an operator-supplied base32 secret. Whitespace,
// dashes and padding are tolerated so a secret pasted from a QR-code tool in
// "abcd efgh" or "ABCD-EFGH" form still works.
func decodeTOTPSecret(s string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '=', '\t', '\n', '\r':
			return -1
		}
		return r
	}, strings.ToUpper(s))
	if clean == "" {
		return nil, fmt.Errorf("empty secret")
	}
	key, err := totpEncoding.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("not base32: %w", err)
	}
	if len(key) < 10 {
		return nil, fmt.Errorf("secret too short (%d bytes; want at least 10)", len(key))
	}
	return key, nil
}

// newTOTPSecret mints a fresh 160-bit secret in the base32 form expected by
// totp_secret and by authenticator apps.
func newTOTPSecret() (string, error) {
	key := make([]byte, 20)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return totpEncoding.EncodeToString(key), nil
}

// totpURI builds the otpauth:// provisioning URI that authenticator apps
// import (usually via a QR code).
func totpURI(issuer, account, secret string) string {
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(int(totpStep.Seconds())))
	label := url.PathEscape(issuer + ":" + account)
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// totpCounter maps a wall-clock instant to its 30-second step number.
func totpCounter(t time.Time) int64 {
	return t.Unix() / int64(totpStep.Seconds())
}

// totpCode computes the 6-digit code for one step (RFC 4226 dynamic
// truncation over HMAC-SHA1).
func totpCode(key []byte, counter int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, bin%1000000)
}

// totpMatch reports which step within ±totpSkew of now the code belongs to,
// or -1 if none. Comparison is constant-time per candidate and every
// candidate is always checked, so timing leaks nothing about which step hit.
func totpMatch(key []byte, code string, now time.Time) int64 {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return -1
	}
	base := totpCounter(now)
	var hit int64 = -1
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		c := base + d
		if subtle.ConstantTimeCompare([]byte(code), []byte(totpCode(key, c))) == 1 {
			hit = c
		}
	}
	return hit
}

// NewTOTPEnrolment mints a fresh secret in the form basic_auth.totp_secret
// expects, together with the otpauth:// URI an authenticator app imports to
// enrol it (render the URI as a QR code, or paste the secret by hand).
func NewTOTPEnrolment(issuer, account string) (secret, uri string, err error) {
	secret, err = newTOTPSecret()
	if err != nil {
		return "", "", err
	}
	return secret, totpURI(issuer, account, secret), nil
}
