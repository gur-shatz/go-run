package supervisor

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// ErrSignatureInvalid is returned by VerifyArchive when the signature does not
// verify against the supplied public key.
var ErrSignatureInvalid = errors.New("signature verification failed")

// LoadPublicKeyFile reads a PEM-encoded Ed25519 public key from path. The PEM
// block type is expected to be "PUBLIC KEY" (PKIX/SPKI). A vendor that ships
// a raw 32-byte key should encode it via PKIX before publishing.
func LoadPublicKeyFile(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pubkey %s: %w", path, err)
	}
	return ParsePublicKey(data)
}

// ParsePublicKey accepts either a PEM-encoded PKIX public key, a base64-naked
// PKIX DER blob, or a raw 32-byte Ed25519 public key.
func ParsePublicKey(data []byte) (ed25519.PublicKey, error) {
	if block, _ := pem.Decode(data); block != nil {
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("unexpected PEM block %q (want PUBLIC KEY)", block.Type)
		}
		return parsePKIXEd25519(block.Bytes)
	}
	// Raw 32-byte public key.
	if len(data) == ed25519.PublicKeySize {
		pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
		copy(pub, data)
		return pub, nil
	}
	// Last-chance: PKIX DER without PEM wrapping.
	return parsePKIXEd25519(data)
}

func parsePKIXEd25519(der []byte) (ed25519.PublicKey, error) {
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse PKIX public key: %w", err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, want ed25519.PublicKey", key)
	}
	return pub, nil
}

// VerifyArchive returns nil if sig is a valid Ed25519 signature of archive
// under pub. Returns ErrSignatureInvalid on a verification miss; any other
// error indicates a structural problem with the inputs.
func VerifyArchive(archive, sig []byte, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("signature is %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pub, archive, sig) {
		return ErrSignatureInvalid
	}
	return nil
}
