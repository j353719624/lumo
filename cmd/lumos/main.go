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
)

func main() {
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
	_ = fs.Parse(os.Args[1:])

	cfg, err := config.LoadServer(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.New(cfg).Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
