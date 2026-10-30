package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/j353719624/lumo/pkg/manager"
	"github.com/j353719624/lumo/pkg/proto"
)

// runInit writes the initial connection config so a fresh install is usable
// right away: `lumoc init -c store.json --server 1.2.3.4 --port 58619
// --token <token>`. It never overwrites an existing config — the caller
// decides whether to append or replace.
func runInit(args []string) {
	fs := flag.NewFlagSet("lumoc init", flag.ExitOnError)
	storePath := fs.String("store", "lumo-configs.json", "connection configs data file")
	server := fs.String("server", "", "server address")
	port := fs.Int("port", 0, "server port")
	token := fs.String("token", "", "auth token")
	protocol := fs.String("protocol", "tcp", "transport protocol (tcp/kcp)")
	kcpPort := fs.Int("kcp-port", 0, "kcp port (when protocol=kcp)")
	tls := fs.Bool("tls", false, "enable tls")
	name := fs.String("name", "", "display name (default: server address)")
	force := fs.Bool("force", false, "replace the existing config instead of appending")
	_ = fs.Parse(args)

	if *server == "" || *port <= 0 || *token == "" {
		fmt.Fprintln(os.Stderr, "lumoc init: --server, --port and --token are required")
		os.Exit(1)
	}
	if *name == "" {
		*name = *server
	}

	store, err := manager.LoadStore(*storePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lumoc init:", err)
		os.Exit(1)
	}
	cfg := &manager.ConnConfig{
		ID:            proto.RandomHex(4),
		Name:          *name,
		ServerAddr:    *server,
		ServerPort:    *port,
		Token:         *token,
		Protocol:      *protocol,
		KCPPort:       *kcpPort,
		TLS:           *tls,
		Enabled:       true,
		AutoReconnect: true,
		AutoStart:     true,
		Mappings:      []manager.Mapping{},
	}
	if *force {
		for _, c := range store.List() {
			_ = store.Delete(c.ID)
		}
	}
	if err := store.Upsert(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "lumoc init:", err)
		os.Exit(1)
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Println(string(out))
}
