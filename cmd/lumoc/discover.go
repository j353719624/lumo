// Command discover connects to the server, authenticates with the token
// (pubkey mode, same as the engine) and prints the sealed-tcp/sealed-udp services
// registered by other clients as JSON — for the desktop visitor shell.
package main

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/proto"
	"github.com/j353719624/lumo/pkg/token"
)

func runDiscover(args []string) {
	fs := flag.NewFlagSet("lumoc discover", flag.ExitOnError)
	cfgPath := fs.String("c", "", "configuration file")
	timeout := fs.Int("t", 6, "timeout seconds")
	_ = fs.Parse(args)
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "lumoc discover: -c <config> is required")
		os.Exit(1)
	}
	cfg, err := config.LoadClient(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lumoc discover:", err)
		os.Exit(1)
	}

	deadline := time.Now().Add(time.Duration(*timeout) * time.Second)
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(cfg.ServerAddr, fmt.Sprint(cfg.ServerPort)), time.Duration(*timeout)*time.Second)
	if err != nil {
		fail(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	hello := proto.Msg{
		"type":   "hello",
		"client": "discover-" + randHex(6),
		"nonce":  fmt.Sprintf("%x", nonce),
		"authp":  token.SignProof(cfg.AuthToken, nonce),
		"pv":     2,
	}
	if err := proto.Send(conn, hello); err != nil {
		fail(err)
	}
	first, err := proto.Recv(conn)
	if err != nil {
		fail(err)
	}
	if first.Str("type") == "error" {
		fail(fmt.Errorf("server: %s", first.Str("message")))
	}
	if first.Str("type") != "services" && first.Str("type") != "welcome" {
		fail(fmt.Errorf("unexpected reply type %q", first.Str("type")))
	}

	if err := proto.Send(conn, proto.Msg{"type": "list-services"}); err != nil {
		fail(err)
	}
	reply, err := proto.Recv(conn)
	if err != nil {
		fail(err)
	}
	out, _ := json.MarshalIndent(reply["services"], "", "  ")
	fmt.Println(string(out))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "lumoc discover:", err)
	os.Exit(1)
}

func randHex(n int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = hex[b[i]%16]
	}
	return string(b)
}
