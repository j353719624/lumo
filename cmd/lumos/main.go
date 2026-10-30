// Command lumos is the LUMO tunnel server.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/server"
	"github.com/j353719624/lumo/pkg/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version.Version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "verify" {
		fs := flag.NewFlagSet("lumos verify", flag.ExitOnError)
		cfgPath := fs.String("c", "lumos.toml", "configuration file")
		_ = fs.Parse(os.Args[2:])
		if _, err := config.LoadServer(*cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "lumos: configuration check failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("lumos: the configuration file %s is ok\n", *cfgPath)
		return
	}

	fs := flag.NewFlagSet("lumos", flag.ExitOnError)
	cfgPath := fs.String("c", "lumos.toml", "configuration file")
	// 网关式部署（fnOS 应用包）：控制台挂在 URL 前缀下，并额外监听
	// 一个 unix socket 供应用网关转发；不传时行为与直连部署完全一致。
	webBase := fs.String("web-base", "", "console URL prefix (e.g. /app/lumo-server)")
	webSocket := fs.String("web-socket", "", "extra unix socket for the console (app gateway)")
	_ = fs.Parse(os.Args[1:])

	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := server.New(cfg)
	srv.SetWebBase(*webBase)
	srv.SetWebSocket(*webSocket)
	if err := srv.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
