// Command engine runs one tunnel connection as a standalone process and
// exposes a localhost-only status endpoint for the console supervisor.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"context"
	"syscall"

	"github.com/j353719624/lumo/pkg/client"
	"github.com/j353719624/lumo/pkg/config"
)

// runEngine implements `lumoc engine -c <toml> --admin <addr>`.
func runEngine(args []string) {
	fs := flag.NewFlagSet("lumoc engine", flag.ExitOnError)
	cfgPath := fs.String("c", "", "engine configuration file")
	admin := fs.String("admin", "127.0.0.1:0", "local status endpoint")
	_ = fs.Parse(args)
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, "lumoc engine: -c <config> is required")
		os.Exit(1)
	}
	cfg, err := config.LoadClient(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lumoc engine:", err)
		os.Exit(1)
	}

	cl := client.New(cfg, *cfgPath)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		running, lastErr, proxies := cl.State()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"running": running,
			"error":   lastErr,
			"proxies": proxies,
		})
	})
	ln, err := net.Listen("tcp", *admin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lumoc engine: admin endpoint:", err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_ = cl.Run(ctx)
	_ = srv.Close()
}
