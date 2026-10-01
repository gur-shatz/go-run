package supervisor

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TOTP", func() {
	// RFC 6238 appendix B test vectors (SHA1, secret "12345678901234567890"),
	// truncated from the 8-digit reference values to 6 digits.
	rfcKey := []byte("12345678901234567890")

	It("matches the RFC 6238 reference vectors", func() {
		Expect(totpCode(rfcKey, totpCounter(time.Unix(59, 0)))).To(Equal("287082"))
		Expect(totpCode(rfcKey, totpCounter(time.Unix(1111111109, 0)))).To(Equal("081804"))
		Expect(totpCode(rfcKey, totpCounter(time.Unix(1234567890, 0)))).To(Equal("005924"))
		Expect(totpCode(rfcKey, totpCounter(time.Unix(20000000000, 0)))).To(Equal("353130"))
	})

	It("round-trips a generated secret through the operator-facing encoding", func() {
		secret, err := newTOTPSecret()
		Expect(err).NotTo(HaveOccurred())
		Expect(secret).NotTo(ContainSubstring("="))
		key, err := decodeTOTPSecret(secret)
		Expect(err).NotTo(HaveOccurred())
		Expect(key).To(HaveLen(20))
	})

	It("tolerates spaces, dashes, padding and lowercase in a pasted secret", func() {
		key, err := decodeTOTPSecret("gezd gnbv-gy3tqojq GEZDGNBVGY3TQOJQ==")
		Expect(err).NotTo(HaveOccurred())
		Expect(key).To(Equal(rfcKey))
	})

	It("rejects garbage and short secrets", func() {
		_, err := decodeTOTPSecret("not!base32")
		Expect(err).To(HaveOccurred())
		_, err = decodeTOTPSecret("GEZD")
		Expect(err).To(MatchError(ContainSubstring("too short")))
		_, err = decodeTOTPSecret("")
		Expect(err).To(HaveOccurred())
	})

	It("accepts one step of drift either side and no more", func() {
		now := time.Unix(1234567890, 0)
		c := totpCounter(now)
		Expect(totpMatch(rfcKey, totpCode(rfcKey, c), now)).To(Equal(c))
		Expect(totpMatch(rfcKey, totpCode(rfcKey, c-1), now)).To(Equal(c - 1))
		Expect(totpMatch(rfcKey, totpCode(rfcKey, c+1), now)).To(Equal(c + 1))
		Expect(totpMatch(rfcKey, totpCode(rfcKey, c-2), now)).To(Equal(int64(-1)))
		Expect(totpMatch(rfcKey, totpCode(rfcKey, c+2), now)).To(Equal(int64(-1)))
		Expect(totpMatch(rfcKey, "12345", now)).To(Equal(int64(-1)))
		Expect(totpMatch(rfcKey, " 005924 ", now)).To(Equal(c))
	})

	It("builds an otpauth URI authenticator apps can import", func() {
		uri := totpURI("supervisor", "admin", "GEZDGNBVGY3TQOJQ")
		Expect(uri).To(HavePrefix("otpauth://totp/supervisor:admin?"))
		Expect(uri).To(ContainSubstring("secret=GEZDGNBVGY3TQOJQ"))
		Expect(uri).To(ContainSubstring("issuer=supervisor"))
		Expect(uri).To(ContainSubstring("digits=6"))
		Expect(uri).To(ContainSubstring("period=30"))
	})
})
