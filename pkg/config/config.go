// Package config loads and validates LUMO configuration files.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/j353719624/lumo/pkg/toml"
)

// WebServer configures the embedded admin dashboard.
type WebServer struct {
	Addr       string
	Port       int
	User       string
	Password   string
	TOTPSecret string // base32; empty disables two-factor login
}

// ServerConfig is the lumos (server) configuration.
type ServerConfig struct {
	ConfigPath     string
	BindPort       int
	VhostHTTPPort  int
	VhostHTTPSPort int
	MuxPort     int
	KCPPort        int
	AuthToken      string
	Web            WebServer
}

// ProxyConfig describes one tunnel registered by a client.
type ProxyConfig struct {
	Name string
	// Label is an optional display name shown in the server console.
	Label         string
	Type          string
	LocalIP       string
	LocalPort     int
	RemotePort    int
	SK            string // secret key for sealed-tcp/sealed-udp
	CustomDomains []string
	// SubPaths narrows an http(s) proxy to specific request paths (e.g. /,/api).
	SubPaths []string
	// Bandwidth caps this proxy's throughput on the client, e.g. "1MB", "500KB".
	Bandwidth string
	// HTTPUser/HTTPPass enable HTTP Basic Auth on the public http entry.
	HTTPUser string
	HTTPPass string
	// Compress enables deflate framing on this proxy's tunnel channel.
	Compress bool
}

// VisitorConfig is a local listener that reaches an sealed-tcp/sealed-udp proxy without
// exposing a public port.
type VisitorConfig struct {
	Name       string // visitor label
	ServerName string // target sealed-tcp/sealed-udp proxy name
	SK         string
	Type       string // tcp (sealed-tcp) or udp (sealed-udp)
	BindAddr   string
	BindPort   int
}

// ClientConfig is the lumoc (client) configuration.
type ClientConfig struct {
	// Name is an optional display name shown in the server console.
	Name       string
	ServerAddr string
	ServerPort int
	// Protocol selects the transport to the server: "tcp" (default) or "kcp".
	Protocol string
	// KCPPort is the server's UDP tunnel port, used when Protocol is "kcp".
	KCPPort int
	// ProtocolVersion selects the tunnel protocol generation: 1 = legacy
	// wire behavior, 2 (default) = extended (compressed data channels).
	ProtocolVersion int
	// StunServer is used for PUNCH hole punching (host:port).
	StunServer string
	AuthToken  string
	TLSEnable  bool
	Web        WebServer
	Proxies    []ProxyConfig
	Visitors   []VisitorConfig
}

// ValidProxyTypes lists the proxy kinds this build supports.
var ValidProxyTypes = map[string]bool{
	"tcp":    true,
	"udp":    true,
	"http":   true,
	"https":  true,
	"mux": true,
	"sealed-tcp":   true,
	"sealed-udp":   true,
	"punch":   true,
}

// LoadServer reads and validates a lumos config file.
func LoadServer(path string) (*ServerConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	root, err := toml.Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c := &ServerConfig{ConfigPath: path}
	c.BindPort = getInt(root, "bindPort", 7000)
	c.VhostHTTPPort = getInt(root, "vhostHTTPPort", 0)
	c.VhostHTTPSPort = getInt(root, "vhostHTTPSPort", 0)
	c.MuxPort = getInt(root, "muxPort", 0)
	c.KCPPort = getInt(root, "kcpPort", 0)
	if a := getTable(root, "auth"); a != nil {
		c.AuthToken = getStr(a, "token", "")
	}
	c.Web = loadWeb(root, WebServer{Addr: "127.0.0.1"})
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *ServerConfig) validate() error {
	if c.BindPort < 1 || c.BindPort > 65535 {
		return fmt.Errorf("bindPort %d out of range", c.BindPort)
	}
	if c.AuthToken == "" {
		return fmt.Errorf("auth.token is required")
	}
	for name, port := range map[string]int{"vhostHTTPPort": c.VhostHTTPPort, "vhostHTTPSPort": c.VhostHTTPSPort, "muxPort": c.MuxPort} {
		if port != 0 && (port < 1 || port > 65535) {
			return fmt.Errorf("%s %d out of range", name, port)
		}
	}
	if c.Web.Port != 0 && (c.Web.Port < 1 || c.Web.Port > 65535) {
		return fmt.Errorf("webServer.port %d out of range", c.Web.Port)
	}
	return nil
}

// LoadClient reads and validates a lumoc config file.
func LoadClient(path string) (*ClientConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	root, err := toml.Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c := &ClientConfig{}
	c.Name = getStr(root, "name", "")
	c.ServerAddr = getStr(root, "serverAddr", "127.0.0.1")
	c.ServerPort = getInt(root, "serverPort", 7000)
	c.Protocol = getStr(root, "protocol", "tcp")
	c.KCPPort = getInt(root, "kcpPort", 0)
	c.ProtocolVersion = getInt(root, "protocolVersion", 2)
	c.StunServer = getStr(root, "stunServer", "stun.easyvoip.com:3478")
	if a := getTable(root, "auth"); a != nil {
		c.AuthToken = getStr(a, "token", "")
	}
	c.Web = loadWeb(root, WebServer{Addr: "127.0.0.1"})
	for _, item := range getTables(root, "proxies") {
		p := ProxyConfig{
			Name:          getStr(item, "name", ""),
			Label:         getStr(item, "label", ""),
			Type:          getStr(item, "type", ""),
			LocalIP:       getStr(item, "localIP", "127.0.0.1"),
			LocalPort:     getInt(item, "localPort", 0),
			RemotePort:    getInt(item, "remotePort", 0),
			SK:            getStr(item, "sk", ""),
			CustomDomains: getStrs(item, "customDomains"),
			SubPaths:      getStrs(item, "subPaths"),
			Bandwidth:     getStr(item, "bandwidth", ""),
			HTTPUser:      getStr(item, "httpUser", ""),
			HTTPPass:      getStr(item, "httpPass", ""),
			Compress:      getBool(item, "compress"),
		}
		c.Proxies = append(c.Proxies, p)
	}
	for _, item := range getTables(root, "visitors") {
		v := VisitorConfig{
			Name:       getStr(item, "name", ""),
			ServerName: getStr(item, "serverName", ""),
			SK:         getStr(item, "sk", ""),
			Type:       getStr(item, "type", "tcp"),
			BindAddr:   getStr(item, "bindAddr", "127.0.0.1"),
			BindPort:   getInt(item, "bindPort", 0),
		}
		c.Visitors = append(c.Visitors, v)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks the client configuration; it is exported so dynamically
// built configurations (e.g. from the console) can be checked too.
func (c *ClientConfig) Validate() error {
	if c.ServerAddr == "" {
		return fmt.Errorf("serverAddr is required")
	}
	if c.ServerPort < 1 || c.ServerPort > 65535 {
		return fmt.Errorf("serverPort %d out of range", c.ServerPort)
	}
	switch c.Protocol {
	case "", "tcp", "kcp":
	default:
		return fmt.Errorf("protocol %q not supported yet (tcp/kcp)", c.Protocol)
	}
	if c.Protocol == "kcp" && (c.KCPPort < 1 || c.KCPPort > 65535) {
		return fmt.Errorf("kcpPort %d out of range", c.KCPPort)
	}
	if c.AuthToken == "" {
		return fmt.Errorf("auth.token is required")
	}
	if c.Web.Port != 0 && (c.Web.Port < 1 || c.Web.Port > 65535) {
		return fmt.Errorf("webServer.port %d out of range", c.Web.Port)
	}
	seen := map[string]bool{}
	for i := range c.Proxies {
		p := &c.Proxies[i]
		if p.Name == "" {
			return fmt.Errorf("proxies[%d]: name is required", i)
		}
		if seen[p.Name] {
			return fmt.Errorf("proxies[%d]: duplicate name %q", i, p.Name)
		}
		seen[p.Name] = true
		if !ValidProxyTypes[p.Type] {
			return fmt.Errorf("proxy %q: unsupported type %q", p.Name, p.Type)
		}
		if p.LocalPort < 1 || p.LocalPort > 65535 {
			return fmt.Errorf("proxy %q: localPort %d out of range", p.Name, p.LocalPort)
		}
		switch p.Type {
		case "tcp", "udp":
			if p.RemotePort < 1 || p.RemotePort > 65535 {
				return fmt.Errorf("proxy %q: remotePort %d out of range", p.Name, p.RemotePort)
			}
		case "http", "https":
			if len(p.CustomDomains) == 0 {
				return fmt.Errorf("proxy %q: customDomains is required for %s", p.Name, p.Type)
			}
		case "mux":
			if len(p.CustomDomains) == 0 {
				return fmt.Errorf("proxy %q: customDomains is required for mux", p.Name)
			}
		case "sealed-tcp", "sealed-udp", "punch":
			if p.SK == "" {
				return fmt.Errorf("proxy %q: sk is required for %s", p.Name, p.Type)
			}
		}
	}
	return nil
}

func loadWeb(root map[string]any, def WebServer) WebServer {
	w := def
	if t := getTable(root, "webServer"); t != nil {
		w.Addr = getStr(t, "addr", def.Addr)
		w.Port = getInt(t, "port", 0)
		w.User = getStr(t, "user", "admin")
		w.Password = getStr(t, "password", "admin")
		w.TOTPSecret = getStr(t, "totpSecret", "")
	}
	return w
}

// ---------- typed accessors over the parsed map ----------

func getTable(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

func getTables(m map[string]any, key string) []map[string]any {
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if t, ok := item.(map[string]any); ok {
			out = append(out, t)
		}
	}
	return out
}

func getInt(m map[string]any, key string, def int) int {
	if v, ok := m[key].(int64); ok {
		return int(v)
	}
	return def
}

func getStr(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return def
}

func getBool(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func getStrs(m map[string]any, key string) []string {
	raw, _ := m[key].([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SaveServer writes a ServerConfig back to a toml file.
func SaveServer(path string, c *ServerConfig) error {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("bindPort = %d\n", c.BindPort))
	if c.VhostHTTPPort > 0 {
		b.WriteString(fmt.Sprintf("vhostHTTPPort = %d\n", c.VhostHTTPPort))
	}
	if c.VhostHTTPSPort > 0 {
		b.WriteString(fmt.Sprintf("vhostHTTPSPort = %d\n", c.VhostHTTPSPort))
	}
	b.WriteString("\n[auth]\n")
	b.WriteString(fmt.Sprintf("token = %q\n", c.AuthToken))
	b.WriteString("\n[webServer]\n")
	b.WriteString(fmt.Sprintf("addr = %q\n", c.Web.Addr))
	b.WriteString(fmt.Sprintf("port = %d\n", c.Web.Port))
	b.WriteString(fmt.Sprintf("user = %q\n", c.Web.User))
	b.WriteString(fmt.Sprintf("password = %q\n", c.Web.Password))
	if c.Web.TOTPSecret != "" {
		b.WriteString(fmt.Sprintf("totpSecret = %q\n", c.Web.TOTPSecret))
	}
	return os.WriteFile(path, []byte(b.String()), 0600)
}
