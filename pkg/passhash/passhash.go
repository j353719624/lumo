// Package passhash stores login passwords as salted PBKDF2-SHA256 hashes
// so that a stolen config file does not reveal any usable password.
//
// Stored format: $pbkdf2$<iterations>$<salt-b64>$<derived-key-b64>
// Legacy plaintext values are accepted by Verify (constant-time compare)
// so existing deployments keep working; callers migrate them to hashes
// on load. The zero-dependency choice (no x/crypto) is deliberate: the
// module carries no third-party modules at all.
package passhash

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"
)

// Iterations for new hashes. ~100ms on a typical NAS CPU — fine for
// login rates, expensive for offline guessing.
const Iterations = 210000

const prefix = "$pbkdf2$"

// IsHash reports whether stored looks like a hash produced by Hash.
func IsHash(stored string) bool {
	return strings.HasPrefix(stored, prefix)
}

// Hash derives a salted PBKDF2-SHA256 hash of pw.
func Hash(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2Key([]byte(pw), salt, Iterations, 32)
	return prefix + strconv.Itoa(Iterations) + "$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(dk), nil
}

// Verify checks pw against stored, which may be a hash (preferred) or a
// legacy plaintext value.
func Verify(pw, stored string) bool {
	if !IsHash(stored) {
		return subtle.ConstantTimeCompare([]byte(pw), []byte(stored)) == 1
	}
	// verified caches (presented credential, stored hash) pairs that
	// already passed, so per-request Basic auth does not pay 100ms of
	// PBKDF2 every time. Keys are fast hashes of the pair — the
	// plaintext password itself is never kept.
	key := pairKey(pw, stored)
	if verified.get(key) {
		return true
	}
	parts := strings.Split(stored, "$") // "", "pbkdf2", iters, salt, dk
	if len(parts) != 5 {
		return false
	}
	iters, err := strconv.Atoi(parts[2])
	if err != nil || iters < 1 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(want) < 16 {
		return false
	}
	got := pbkdf2Key([]byte(pw), salt, iters, len(want))
	if !hmac.Equal(got, want) {
		return false
	}
	verified.put(key)
	return true
}

// pairKey is a fast, non-reversible cache key for a verified
// (presented password, stored hash) pair.
func pairKey(pw, stored string) string {
	sum := sha256.Sum256([]byte(pw + "\x00" + stored))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

type cache struct {
	mu sync.RWMutex
	m  map[string]struct{}
}

var verified = &cache{m: make(map[string]struct{})}

func (c *cache) get(k string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.m[k]
	return ok
}

func (c *cache) put(k string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 1024 { // bound memory; simply start over
		c.m = make(map[string]struct{})
	}
	c.m[k] = struct{}{}
}

// pbkdf2Key implements PBKDF2 (RFC 8018) with HMAC-SHA256.
func pbkdf2Key(pw, salt []byte, iters, klen int) []byte {
	dk := make([]byte, 0, klen)
	var block [4]byte
	for i := 1; len(dk) < klen; i++ {
		block[0], block[1], block[2], block[3] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		prf := hmac.New(sha256.New, pw)
		prf.Write(salt)
		prf.Write(block[:])
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 1; n < iters; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:klen]
}
