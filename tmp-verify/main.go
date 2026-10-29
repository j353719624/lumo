package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/j353719624/lumo/pkg/token"
)

func main() {
	secret := os.Args[1]
	pub := os.Args[2]
	nonceHex := os.Args[3]
	sig := os.Args[4]
	nonce, _ := hex.DecodeString(nonceHex)
	_ = nonce
	fmt.Println("derive-match:", token.DerivePublicKey(secret) == pub)
	fmt.Println("verify:", token.VerifyProof(pub, mustHex(nonceHex), sig))
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
