package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/j353719624/lumo/pkg/token"
)

// runServerInit writes a ready-to-run server configuration. The secret is
// stored only as its derived public key, so the tunnel secret never lands on
// disk in plain text; the same secret is printed once for the operator to
// put into the client side.
func runServerInit(args []string) {
	fs := flag.NewFlagSet("lumoc server", flag.ExitOnError)
	out := fs.String("out", "lumos.toml", "server config path")
	secret := fs.String("secret", "", "tunnel secret (empty: generate a new one)")
	bindPort := fs.Int("bind-port", 58619, "client connect port")
	kcpPort := fs.Int("kcp-port", 47011, "kcp transport port")
	vhostPort := fs.Int("vhost-port", 18080, "virtual host http port")
	webPort := fs.Int("web-port", 46351, "dashboard port")
	webAddr := fs.String("web-addr", "0.0.0.0", "dashboard listen address")
	webUser := fs.String("web-user", "admin", "dashboard user")
	webPass := fs.String("web-pass", "", "dashboard password (empty: generate)")
	allowPorts := fs.String("allow-ports", "", "comma separated public port allowlist")
	_ = fs.Parse(args)

	sec := strings.TrimSpace(*secret)
	if sec == "" {
		generated, _, err := token.Generate()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lumoc server:", err)
			os.Exit(1)
		}
		sec = generated
	}
	if err := token.CheckStrength(sec); err != nil {
		fmt.Fprintln(os.Stderr, "lumoc server:", err)
		os.Exit(1)
	}
	pass := *webPass
	if pass == "" {
		generated, _, err := token.Generate()
		if err != nil {
			fmt.Fprintln(os.Stderr, "lumoc server:", err)
			os.Exit(1)
		}
		pass = generated[:16]
	}

	var b strings.Builder
	b.WriteString("# LUMO 服务端配置（由 lumoc server 生成）\n")
	b.WriteString(fmt.Sprintf("bindPort = %d\n", *bindPort))
	if *kcpPort > 0 {
		b.WriteString(fmt.Sprintf("kcpPort = %d\n", *kcpPort))
	}
	if *vhostPort > 0 {
		b.WriteString(fmt.Sprintf("vhostHTTPPort = %d\n", *vhostPort))
	}
	if *allowPorts != "" {
		b.WriteString(fmt.Sprintf("allowPorts = %q\n", *allowPorts))
	}
	b.WriteString("\n[auth]\n")
	b.WriteString(fmt.Sprintf("pubKey = %q\n", token.DerivePublicKey(sec)))
	b.WriteString("\n[webServer]\n")
	b.WriteString(fmt.Sprintf("addr = %q\n", *webAddr))
	b.WriteString(fmt.Sprintf("port = %d\n", *webPort))
	b.WriteString(fmt.Sprintf("user = %q\n", *webUser))
	b.WriteString(fmt.Sprintf("password = %q\n", pass))
	b.WriteString("\n")

	if err := os.WriteFile(*out, []byte(b.String()), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "lumoc server:", err)
		os.Exit(1)
	}
	// 机器可读输出：安装脚本靠这几行取密钥与控制台密码
	fmt.Printf("SERVER_CONFIG=%s\n", *out)
	fmt.Printf("SECRET=%s\n", sec)
	fmt.Printf("WEB_USER=%s\n", *webUser)
	fmt.Printf("WEB_PASS=%s\n", pass)
}
