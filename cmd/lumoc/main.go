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
	"syscall"

	"github.com/j353719624/lumo/pkg/client"
	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/manager"
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
	user := fs.String("user", "admin", "console user")
	pass := fs.String("pass", "", "console password (empty: no authentication)")
	base := fs.String("base", "", "URL prefix the console is served under (e.g. /app/lumo)")
	_ = fs.Parse(args)

	store, err := manager.LoadStore(*storePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mgr := manager.New(store)
	mgr.SetBase(*base)
	if err := mgr.Run(*addr, *user, *pass); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
