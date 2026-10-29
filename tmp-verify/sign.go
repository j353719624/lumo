package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/j353719624/lumo/pkg/proto"
)

func main() {
	secret := os.Args[1]
	nonce, _ := hex.DecodeString(os.Args[2])
	fmt.Println(proto.SignProof(secret, nonce))
}
