package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/j353719624/lumo/pkg/client"
	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/server"
)

// freePort reserves an ephemeral port by binding and closing it again.
func freePort(t *testing.T, network string) int {
	t.Helper()
	switch network {
	case "udp":
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	default:
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := fn()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %v", what, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestTCPTunnel runs a lumos server and a lumoc client in-process and
// verifies that HTTP traffic reaches the local service through the tunnel.
func TestTCPTunnel(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "LUMO_E2E_OK")
	}))
	defer local.Close()
	localPort := local.Listener.Addr().(*net.TCPAddr).Port

	bindPort := freePort(t, "tcp")
	remotePort := freePort(t, "tcp")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverCfg := &config.ServerConfig{BindPort: bindPort, AuthToken: "e2e-token"}
	go func() { _ = server.New(serverCfg).Run(ctx) }()

	waitFor(t, 5*time.Second, "server listener", func() error {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", bindPort), 200*time.Millisecond)
		if err != nil {
			return err
		}
		return c.Close()
	})

	clientCfg := &config.ClientConfig{
		ServerAddr: "127.0.0.1",
		ServerPort: bindPort,
		AuthToken:  "e2e-token",
		Proxies: []config.ProxyConfig{{
			Name: "e2e-tcp", Type: "tcp",
			LocalIP: "127.0.0.1", LocalPort: localPort, RemotePort: remotePort,
		}},
	}
	cl := client.New(clientCfg, "")
	go func() { _ = cl.Run(ctx) }()

	waitFor(t, 15*time.Second, "tunnel response", func() error {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", remotePort))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(b), "LUMO_E2E_OK") {
			return fmt.Errorf("unexpected body: %q", b)
		}
		return nil
	})
}

// TestHTTPVhost verifies host-header routing through the vhost port.
func TestHTTPVhost(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "VHOST_OK host="+r.Host)
	}))
	defer local.Close()
	localPort := local.Listener.Addr().(*net.TCPAddr).Port

	bindPort := freePort(t, "tcp")
	vhostPort := freePort(t, "tcp")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverCfg := &config.ServerConfig{BindPort: bindPort, VhostHTTPPort: vhostPort, AuthToken: "e2e-token"}
	go func() { _ = server.New(serverCfg).Run(ctx) }()

	waitFor(t, 5*time.Second, "server listener", func() error {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", bindPort), 200*time.Millisecond)
		if err != nil {
			return err
		}
		return c.Close()
	})

	clientCfg := &config.ClientConfig{
		ServerAddr: "127.0.0.1",
		ServerPort: bindPort,
		AuthToken:  "e2e-token",
		Proxies: []config.ProxyConfig{{
			Name: "e2e-http", Type: "http",
			LocalIP: "127.0.0.1", LocalPort: localPort,
			CustomDomains: []string{"e2e.example.com"},
		}},
	}
	cl := client.New(clientCfg, "")
	go func() { _ = cl.Run(ctx) }()

	waitFor(t, 15*time.Second, "vhost response", func() error {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", vhostPort), nil)
		req.Host = "e2e.example.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(b), "VHOST_OK") {
			return fmt.Errorf("unexpected body: %q", b)
		}
		return nil
	})
}
