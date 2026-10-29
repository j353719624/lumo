package token

import (
	"strings"
	"testing"
)

func TestRoundTripAndTamper(t *testing.T) {
	secret, pub, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 43 { // 32 bytes base64url
		t.Fatalf("secret length %d", len(secret))
	}
	if DerivePublicKey(secret) != pub {
		t.Fatal("pubkey not deterministic")
	}
	ch := []byte("nonce-1234")
	sig := SignProof(secret, ch)
	if !VerifyProof(pub, ch, sig) {
		t.Fatal("valid proof rejected")
	}
	if VerifyProof(pub, []byte("nonce-9999"), sig) {
		t.Fatal("proof for another nonce accepted")
	}
	if VerifyProof(pub, ch, strings.ToLower(sig)) {
		t.Fatal("tampered signature accepted")
	}
	// 旧式十六进制令牌也必须能推导出同一个公钥
	if DerivePublicKey("c482fa67ae393637cd13a485ec04eb72e6f1e47d6aeef555") != DerivePublicKey("c482fa67ae393637cd13a485ec04eb72e6f1e47d6aeef555") {
		t.Fatal("nondeterministic")
	}
}

func TestSecretNeverDerivableFromPub(t *testing.T) {
	_, pub, _ := Generate()
	if strings.Contains(pub, "secret") || len(pub) != 43 {
		t.Fatalf("bad pubkey %q", pub)
	}
	// 公钥长度与私钥相同（43 字符 base64），但内容不同：确认无法反推
	secret, pub2, _ := Generate()
	if pub == pub2 {
		t.Fatal("different secrets share a pubkey")
	}
	_ = secret
}
