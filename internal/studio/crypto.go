package studio

import (
	"crypto/hmac"
	"crypto/sha256"
)

// signer signs session cookies (HMAC-SHA256) with a key derived from
// STUDIO_SECRET. Changing the secret signs you out.
type signer struct{ key []byte }

func newSigner(secret string) *signer {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("studio session signing"))
	return &signer{key: m.Sum(nil)}
}

func (s *signer) sign(msg []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(msg)
	return m.Sum(nil)
}
