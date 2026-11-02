// Command lumoc is the LUMO tunnel client.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/j353719624/lumo/pkg/client"
	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/manager"
	"github.com/j353719624/lumo/pkg/passhash"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "ui" {
		runConsole(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "engine" {
		runEngine(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "discover" {
		runDiscover(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "init" {
		runInit(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "server" {
		runServerInit(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "token" {
		// 认证令牌由客户端生成；两端填入同一令牌即可互通
		tok, err := newToken()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(tok)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "hashpass" {
		// 供安装脚本把控制台口令直接写成 PBKDF2 哈希，避免任何明文落盘
		if len(os.Args) < 3 || os.Args[2] == "" {
			fmt.Fprintln(os.Stderr, "usage: lumoc hashpass <password>")
			os.Exit(1)
		}
		h, err := passhash.Hash(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(h)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "verify" {
		fs := flag.NewFlagSet("lumoc verify", flag.ExitOnError)
		cfgPath := fs.String("c", "lumoc.toml", "configuration file")
		_ = fs.Parse(os.Args[2:])
		if _, err := config.LoadClient(*cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "lumoc: configuration check failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("lumoc: the configuration file %s is ok\n", *cfgPath)
		return
	}

	fs := flag.NewFlagSet("lumoc", flag.ExitOnError)
	cfgPath := fs.String("c", "lumoc.toml", "configuration file")
	_ = fs.Parse(os.Args[1:])

	cfg, err := config.LoadClient(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cl := client.New(cfg, *cfgPath)
	_ = cl.Run(ctx)
}

// newToken returns a fresh 256-bit hex authentication token.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// runConsole serves the multi-connection console (`lumoc ui`).
func runConsole(args []string) {
	fs := flag.NewFlagSet("lumoc ui", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7400", "console listen address")
	storePath := fs.String("store", "lumo-configs.json", "connection configs data file")
	// 首选 --auth-file：凭证只进文件（含哈希），不进命令行——命令行参数
	// 会在 ps 里对全系统可见，是比配置文件更宽的泄漏面。--user/--pass 等
	// 旧参数保留给手工排障，值同样支持哈希形态。
	authFile := fs.String("auth-file", "", "read console credentials from this KEY=\"VALUE\" env file (recommended)")
	user := fs.String("user", "admin", "console user")
	pass := fs.String("pass", "", "console password or its PBKDF2 hash (empty: no authentication)")
	basicUser := fs.String("basic-user", "", "HTTP basic auth user gating the console (empty: layer off)")
	basicPass := fs.String("basic-pass", "", "HTTP basic auth password or its PBKDF2 hash")
	base := fs.String("base", "", "URL prefix the console is served under (e.g. /app/lumo)")
	socket := fs.String("socket", "", "serve the console on this unix socket instead of TCP")
	_ = fs.Parse(args)

	if *authFile != "" {
		creds, err := readAuthFile(*authFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lumoc ui: 读取凭证文件失败: %v\n", err)
			os.Exit(1)
		}
		if v, ok := creds["CONSOLE_USER"]; ok && v != "" {
			*user = v
		}
		if v, ok := creds["CONSOLE_PASS"]; ok && v != "" {
			*pass = v
		}
		if v, ok := creds["BASIC_USER"]; ok {
			*basicUser = v
		}
		if v, ok := creds["BASIC_PASS"]; ok {
			*basicPass = v
		}
		if err := migrateAuthFile(*authFile, creds); err != nil {
			fmt.Fprintf(os.Stderr, "lumoc ui: 警告：口令哈希写回失败（明文仍生效）: %v\n", err)
		}
	}

	store, err := manager.LoadStore(*storePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mgr := manager.New(store)
	mgr.SetBase(*base)
	mgr.SetSocket(*socket)
	mgr.SetBasicAuth(*basicUser, *basicPass)
	if err := mgr.Run(*addr, *user, *pass); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// readAuthFile parses a shell-sourceable KEY="VALUE" file into a map.
// Lines are KEY=value with optional double quotes; blanks and #comments
// are skipped. Values keep everything inside the quotes verbatim.
func readAuthFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, "\"")
		out[key] = val
	}
	return out, nil
}

// migrateAuthFile rewrites the auth file with PBKDF2 hashes in place of
// any plaintext password, keeping 0600 permissions and the original
// key order. Hashed values and other keys are preserved verbatim.
func migrateAuthFile(path string, creds map[string]string) error {
	changed := false
	for _, key := range []string{"CONSOLE_PASS", "BASIC_PASS"} {
		v := creds[key]
		if v == "" || passhash.IsHash(v) {
			continue
		}
		h, err := passhash.Hash(v)
		if err != nil {
			return err
		}
		creds[key] = h
		changed = true
	}
	if !changed {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		for _, key := range []string{"CONSOLE_PASS", "BASIC_PASS"} {
			if strings.HasPrefix(strings.TrimSpace(line), key+"=") {
				lines[i] = key + "=\"" + creds[key] + "\""
			}
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
