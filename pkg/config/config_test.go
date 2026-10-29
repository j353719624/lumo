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

func TestLoadServerKeepsConfigPath(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/lumos.toml"
	content := "bindPort = 7000\n[auth]\ntoken = \"t\"\n[webServer]\naddr = \"127.0.0.1\"\nport = 7500\nuser = \"admin\"\npassword = \"pw\"\n"
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.ConfigPath != p {
		t.Fatalf("ConfigPath = %q, want %q (needed so the console can persist changes)", c.ConfigPath, p)
	}
}

// 回归：控制台保存（2FA 绑定/解绑、面板保存）走 SaveServer，
// 任何一次保存都不能丢掉 pubKey/kcpPort/revision 等其余字段。
func TestSaveServerRoundTripKeepsAllFields(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/lumos.toml"
	c := &ServerConfig{
		ConfigPath: p, BindPort: 58619, KCPPort: 47011,
		VhostHTTPPort: 18080, SubdomainHost: "tunnel.example.com",
		AllowPorts: "1000-2000,3000", AuthPubKey: "pubkey-b64", Revision: 7,
		Web: WebServer{Addr: "0.0.0.0", Port: 46351, User: "u", Password: "p", TOTPSecrets: []string{"SECRET234", "SECRET567"}},
	}
	if err := SaveServer(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadServer(p)
	if err != nil {
		t.Fatalf("round-trip reload failed: %v", err)
	}
	if got.BindPort != 58619 || got.KCPPort != 47011 || got.VhostHTTPPort != 18080 ||
		got.SubdomainHost != "tunnel.example.com" || got.AllowPorts != "1000-2000,3000" ||
		got.AuthPubKey != "pubkey-b64" || got.Revision != 7 ||
		got.Web.Port != 46351 || len(got.Web.TOTPSecrets) != 2 || got.Web.TOTPSecrets[0] != "SECRET234" || got.Web.TOTPSecrets[1] != "SECRET567" {
		t.Fatalf("fields lost on save: %+v", got)
	}

	// 解绑全部 2FA = 清空 TOTPSecrets 再保存：pubKey/kcpPort/revision 必须原样保留
	c.Web.TOTPSecrets = nil
	if err := SaveServer(p, c); err != nil {
		t.Fatal(err)
	}
	got, err = LoadServer(p)
	if err != nil {
		t.Fatalf("reload after 2FA unbind failed: %v", err)
	}
	if got.AuthPubKey != "pubkey-b64" || got.KCPPort != 47011 || got.Revision != 7 || len(got.Web.TOTPSecrets) != 0 {
		t.Fatalf("2FA unbind lost or resurrected fields: %+v", got)
	}

	// 明文令牌模式：写 token 而不是空 pubKey
	c2 := &ServerConfig{BindPort: 7000, AuthToken: "plain", Web: WebServer{Addr: "127.0.0.1", User: "a", Password: "b"}}
	p2 := dir + "/plain.toml"
	if err := SaveServer(p2, c2); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServer(p2); err != nil {
		t.Fatalf("plain token round-trip failed: %v", err)
	}

	// 两种凭据都没有：拒绝写出无法登录的配置
	c3 := &ServerConfig{BindPort: 7000, Web: WebServer{Addr: "127.0.0.1"}}
	p3 := dir + "/bad.toml"
	if err := SaveServer(p3, c3); err == nil {
		t.Fatal("expected refusal to write an auth-less config")
	}
	if _, err := os.Stat(p3); err == nil {
		t.Fatal("no file must be written when auth is missing")
	}
}

// 旧版单密钥 totpSecret 配置加载后进入 TOTPSecrets 列表。
func TestLoadServerMigratesLegacyTotpSecret(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/lumos.toml"
	content := "bindPort = 7000\n[auth]\ntoken = \"t\"\n[webServer]\naddr = \"127.0.0.1\"\nport = 7500\nuser = \"admin\"\npassword = \"pw\"\ntotpSecret = \"OLDSECRET\"\n"
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadServer(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Web.TOTPSecrets) != 1 || got.Web.TOTPSecrets[0] != "OLDSECRET" {
		t.Fatalf("legacy totpSecret not migrated: %+v", got.Web.TOTPSecrets)
	}
	if err := SaveServer(p, got); err != nil {
		t.Fatal(err)
	}
	got2, err := LoadServer(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2.Web.TOTPSecrets) != 1 || got2.Web.TOTPSecrets[0] != "OLDSECRET" {
		t.Fatalf("migrated secret lost after save: %+v", got2.Web.TOTPSecrets)
	}
}
