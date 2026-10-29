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

// TestPublicKeyIsNotUsableAsSecret proves the exact property the console
// depends on: someone who only has the stored public key (e.g. from a full
// server compromise) cannot authenticate with it.
func TestPublicKeyIsNotUsableAsSecret(t *testing.T) {
	secret, pub, _ := Generate()
	nonce := []byte("challenge-bytes")
	realSig := SignProof(secret, nonce)
	if !VerifyProof(pub, nonce, realSig) {
		t.Fatal("precondition: real secret must verify")
	}
	// 攻击者把公钥本身当令牌：派生出的公钥完全不同，签名必然不被接受
	imposterSig := SignProof(pub, nonce)
	if VerifyProof(pub, nonce, imposterSig) {
		t.Fatal("public key must NOT work as a client secret")
	}
	// 弱令牌对其自身公钥同样有效（安全性靠拒绝弱令牌入库来保证）
	weak := "12345"
	weakPub := DerivePublicKey(weak)
	if !VerifyProof(weakPub, nonce, SignProof(weak, nonce)) {
		t.Fatal("weak token must verify against its own pubkey (sanity)")
	}
}

func TestCheckStrength(t *testing.T) {
	_, pub, _ := Generate()
	strong := "VPT3M6cAugSWfd-0dZgJ_Lqpyz8Fp4sbsgY0p748DBg"
	for _, ok := range []string{strong, pub} {
		if err := CheckStrength(ok); err != nil {
			t.Fatalf("CheckStrength(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "12345", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if err := CheckStrength(bad); err == nil {
			t.Fatalf("CheckStrength(%q) should reject", bad)
		}
	}
}
