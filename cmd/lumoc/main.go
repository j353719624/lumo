// Command lumoc is the LUMO tunnel client.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"lumo/internal/client"
	"lumo/internal/config"
)

func main() {
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
