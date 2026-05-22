package supervisor_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/pkg/supervisor"
)

var _ = Describe("VerifyArchive", func() {
	var (
		pub  ed25519.PublicKey
		priv ed25519.PrivateKey
	)

	BeforeEach(func() {
		var err error
		pub, priv, err = ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts a correct signature", func() {
		body := []byte("any tarball contents")
		sig := ed25519.Sign(priv, body)

		Expect(supervisor.VerifyArchive(body, sig, pub)).To(Succeed())
	})

	It("rejects a tampered archive", func() {
		body := []byte("any tarball contents")
		sig := ed25519.Sign(priv, body)
		body[0] ^= 0xFF

		Expect(supervisor.VerifyArchive(body, sig, pub)).To(MatchError(supervisor.ErrSignatureInvalid))
	})

	It("rejects a signature from a different key", func() {
		_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())

		body := []byte("contents")
		badSig := ed25519.Sign(otherPriv, body)

		Expect(supervisor.VerifyArchive(body, badSig, pub)).To(MatchError(supervisor.ErrSignatureInvalid))
	})

	It("errors on a signature of the wrong length", func() {
		err := supervisor.VerifyArchive([]byte("x"), []byte{1, 2, 3}, pub)
		Expect(err).To(MatchError(ContainSubstring("signature")))
	})

	It("loads a PEM-encoded PKIX public key from disk", func() {
		der, err := x509.MarshalPKIXPublicKey(pub)
		Expect(err).NotTo(HaveOccurred())
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

		path := filepath.Join(GinkgoT().TempDir(), "update.pub")
		Expect(os.WriteFile(path, pemBytes, 0644)).To(Succeed())

		loaded, err := supervisor.LoadPublicKeyFile(path)
		Expect(err).NotTo(HaveOccurred())

		body := []byte("hello")
		sig := ed25519.Sign(priv, body)
		Expect(supervisor.VerifyArchive(body, sig, loaded)).To(Succeed())
	})

	It("accepts a raw 32-byte key", func() {
		loaded, err := supervisor.ParsePublicKey(pub)
		Expect(err).NotTo(HaveOccurred())
		Expect([]byte(loaded)).To(Equal([]byte(pub)))
	})
})
