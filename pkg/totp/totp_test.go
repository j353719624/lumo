package totp

import (
	"testing"
	"time"
)

// RFC 6238 appendix B vectors (SHA-1). The reference secret is the ASCII
// string "12345678901234567890"; our 6-digit code is the last 6 digits of
// the reference 8-digit value.
func TestVerifyRFC6238Vectors(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32("12345678901234567890")
	cases := []struct {
		unix int64
		code string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, c := range cases {
		if !Verify(secret, c.code, time.Unix(c.unix, 0)) {
			t.Errorf("code %s at t=%d should verify", c.code, c.unix)
		}
	}
	if Verify(secret, "287083", time.Unix(59, 0)) {
		t.Error("wrong code should not verify")
	}
	// adjacent-step tolerance: code from t=59 also verifies at t=89
	if !Verify(secret, "287082", time.Unix(89, 0)) {
		t.Error("adjacent step should verify (drift tolerance)")
	}
}

func TestGenerateRoundTrip(t *testing.T) {
	sec, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !Valid(sec) {
		t.Fatalf("generated secret %q not valid", sec)
	}
	now := time.Now()
	c, err := code(sec, uint64(now.Unix())/stepSeconds)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(sec, c, now) {
		t.Fatal("round trip failed")
	}
	if URI("admin", sec) == "" || !Valid("JBSWY3DPEHPK3PXP") {
		t.Fatal("uri/valid sanity failed")
	}
}

func TestBadInput(t *testing.T) {
	if Valid("not!base32!!") {
		t.Error("invalid base32 accepted")
	}
	if Verify("GEZDGNBVGY3TQOJQ", "12345", time.Now()) {
		t.Error("short code accepted")
	}
}
