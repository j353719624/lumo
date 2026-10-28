// Package totp implements RFC 6238 time-based one-time passwords
// (6 digits, 30-second step, SHA-1) using only the standard library.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

const (
	secretBytes = 10 // 80-bit secret, 16 base32 chars
	stepSeconds = 30
	digits      = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Generate returns a new random base32-encoded secret.
func Generate() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b32.EncodeToString(buf), nil
}

// Validate reports whether the base32 secret is decodable.
func Valid(secret string) bool {
	_, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	return err == nil && len(secret) > 0
}

// code computes the OTP for the given counter window.
func code(secret string, counter uint64) (string, error) {
	raw, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", fmt.Errorf("bad secret: %w", err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, raw)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%0*d", digits, bin%1_000_000), nil
}

// Verify checks a user-supplied code against the current step and the
// adjacent ones (clock drift tolerance of ±30s).
func Verify(secret, userCode string, now time.Time) bool {
	userCode = strings.TrimSpace(userCode)
	if len(userCode) != digits {
		return false
	}
	counter := uint64(now.Unix()) / stepSeconds
	for _, d := range []uint64{0, 1, ^uint64(0)} {
		want, err := code(secret, counter+d)
		if err != nil {
			return false
		}
		if hmacEqual(want, userCode) {
			return true
		}
	}
	return false
}

func hmacEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// URI builds the otpauth:// enrollment link authenticator apps scan.
func URI(account, secret string) string {
	return fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&digits=%d&period=%d",
		"LUMO:"+account, secret, "LUMO", digits, stepSeconds)
}
