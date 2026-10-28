package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadServer(t *testing.T) {
	path := write(t, "lumos.toml", `
bindPort = 7000
vhostHTTPPort = 18090

[auth]
token = "s3cret"

[webServer]
addr = "127.0.0.1"
port = 7500
user = "admin"
password = "p"
`)
	cfg, err := LoadServer(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindPort != 7000 || cfg.VhostHTTPPort != 18090 || cfg.AuthToken != "s3cret" {
		t.Errorf("server config mismatch: %+v", cfg)
	}
	if cfg.Web.Port != 7500 || cfg.Web.Password != "p" {
		t.Errorf("web config mismatch: %+v", cfg.Web)
	}
}

func TestLoadServerRequiresToken(t *testing.T) {
	path := write(t, "lumos.toml", "bindPort = 7000\n")
	if _, err := LoadServer(path); err == nil {
		t.Error("expected auth.token validation error")
	}
}

func TestLoadClient(t *testing.T) {
	path := write(t, "lumoc.toml", `
serverAddr = "10.1.2.3"
serverPort = 7001

[auth]
token = "t"

[[proxies]]
name = "ssh"
type = "tcp"
localPort = 22
remotePort = 6022

[[proxies]]
name = "site"
type = "http"
localPort = 8080
customDomains = ["a.example.com"]
`)
	cfg, err := LoadClient(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerAddr != "10.1.2.3" || len(cfg.Proxies) != 2 {
		t.Fatalf("client config mismatch: %+v", cfg)
	}
	if cfg.Proxies[0].RemotePort != 6022 || cfg.Proxies[1].CustomDomains[0] != "a.example.com" {
		t.Errorf("proxy config mismatch: %+v", cfg.Proxies)
	}
}

func TestLoadClientRejectsBadProxy(t *testing.T) {
	path := write(t, "lumoc.toml", `
serverAddr = "127.0.0.1"
[auth]
token = "t"
[[proxies]]
name = "bad"
type = "tcp"
localPort = 80
`)
	if _, err := LoadClient(path); err == nil {
		t.Error("expected remotePort validation error")
	}
}
