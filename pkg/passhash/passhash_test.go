package passhash

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	h, err := Hash("s3cret-密码!")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !IsHash(h) {
		t.Fatalf("IsHash = false for %q", h)
	}
	if !Verify("s3cret-密码!", h) {
		t.Fatal("Verify(correct) = false")
	}
	if Verify("wrong", h) {
		t.Fatal("Verify(wrong) = true")
	}
	// salted: two hashes of the same password differ
	h2, _ := Hash("s3cret-密码!")
	if h == h2 {
		t.Fatal("two hashes of the same password are identical")
	}
}

func TestVerifyLegacyPlaintext(t *testing.T) {
	if !Verify("plainpw", "plainpw") {
		t.Fatal("legacy plaintext verify failed")
	}
	if Verify("nope", "plainpw") {
		t.Fatal("legacy plaintext accepted wrong password")
	}
}

func TestVerifyMalformed(t *testing.T) {
	cases := []string{
		"$pbkdf2$",                    // truncated
		"$pbkdf2$abc$xx$yy",           // bad iters
		"$pbkdf2$0$xx$yy",             // zero iters
		"$pbkdf2$1$!!!$yy",            // bad salt b64
		"$pbkdf2$1$YQ$!!!",            // bad dk b64
		"$pbkdf2$1$YQ$YQ",             // garbage dk
		prefix + "1$YQ$",              // too few parts
	}
	for _, c := range cases {
		if Verify("x", c) {
			t.Fatalf("malformed hash accepted: %q", c)
		}
	}
}

func TestHashFormat(t *testing.T) {
	h, _ := Hash("pw")
	parts := strings.Split(h, "$")
	if len(parts) != 5 || parts[1] != "pbkdf2" {
		t.Fatalf("unexpected format: %q", h)
	}
	if parts[2] != "210000" {
		t.Fatalf("unexpected iteration count: %q", parts[2])
	}
}
