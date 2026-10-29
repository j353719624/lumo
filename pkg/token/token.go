// Package token implements the tunnel authentication credential: a 32-byte
// secret that doubles as an Ed25519 seed. The server stores only the derived
// public key, so a full server compromise yields nothing that can forge a
// client signature or reveal the secret.
package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// Generate returns a fresh 256-bit secret (base64url) and its public key.
func Generate() (secret string, pub string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	secret = base64.RawURLEncoding.EncodeToString(buf)
	return secret, DerivePublicKey(secret), nil
}

// seedOf derives the Ed25519 seed from any secret string (generated
// base64 or a legacy hex token) so every token works the same way.
func seedOf(secret string) ed25519.PrivateKey {
	sum := sha256.Sum256([]byte(secret))
	return ed25519.NewKeyFromSeed(sum[:])
}

// DerivePublicKey returns the base64 public key for a secret.
func DerivePublicKey(secret string) string {
	return base64.RawStdEncoding.EncodeToString(seedOf(secret).Public().(ed25519.PublicKey))
}

// SignProof signs the challenge bytes with the secret.
func SignProof(secret string, challenge []byte) string {
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(seedOf(secret), challenge))
}

// VerifyProof checks a signature against the stored public key.
func VerifyProof(pub string, challenge []byte, sig string) bool {
	key, err := base64.RawStdEncoding.DecodeString(pub)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return false
	}
	s, err := base64.RawStdEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(key), challenge, s)
}

// Fingerprint returns a short, non-reversible identifier of a public key
// (for display in consoles).
func Fingerprint(pub string) string {
	sum := sha256.Sum256([]byte(pub))
	return base64.RawURLEncoding.EncodeToString(sum[:6])
}

var ErrBadSecret = errors.New("token: empty secret")
