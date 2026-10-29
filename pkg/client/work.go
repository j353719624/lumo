package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/proto"
	"github.com/j353719624/lumo/pkg/relay"
)

// handleWork fulfils a work-connection request by dialing the server and
// handing the fresh connection to the proxy's relay implementation.
func (c *Client) handleWork(m proto.Msg) {
	name := m.Str("name")
	c.mu.Lock()
	p := c.proxies[name]
	c.mu.Unlock()
	if p == nil {
		c.logw("work request for unknown proxy %q", name)
		return
	}
	local := net.JoinHostPort(p.LocalIP, strconv.Itoa(p.LocalPort))
	cnt := c.counters(name)

	// mux pairs through the dedicated entry port with an HTTP CONNECT
	// attach instead of a control-port data connection.
	if p.Type == "mux" {
		c.muxRelay(name, m.Str("cid"), local, cnt)
		return
	}

	dc, err := c.dataConn()
	if err != nil {
		c.loge("proxy %q: dial for work connection: %v", name, err)
		return
	}
	if err := proto.Send(dc, proto.Msg{"type": "data", "name": name, "cid": m.Str("cid")}); err != nil {
		c.loge("proxy %q: announce data connection: %v", name, err)
		_ = dc.Close()
		return
	}
	switch p.Type {
	case "tcp", "https", "sealed-tcp":
		c.rawRelay(name, dc, local, cnt)
	case "http":
		c.httpRelay(name, dc, local, cnt)
	case "udp", "sealed-udp":
		c.udpRelay(name, dc, local, cnt)
	}
}

// muxRelay attaches to the server's mux port with the cid issued by
// the work request, then relays to the local service.
func (c *Client) muxRelay(name, cid, local string, cnt *relay.Counters) {
	c.mu.Lock()
	port := c.muxPort
	c.mu.Unlock()
	if port == 0 {
		c.loge("proxy %q: server did not announce a mux port", name)
		return
	}
	addr := net.JoinHostPort(c.cfg.ServerAddr, strconv.Itoa(port))
	dc, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		c.loge("proxy %q: dial mux %s: %v", name, addr, err)
		return
	}
	host := addr
	req := fmt.Sprintf("CONNECT /vc HTTP/1.1\r\nHost: %s\r\nLumo-Name: %s\r\nLumo-Cid: %s\r\nLumo-Token: %s\r\n\r\n",
		host, name, cid, proto.Sign(c.cfg.AuthToken, []byte(cid)))
	if _, err := dc.Write([]byte(req)); err != nil {
		_ = dc.Close()
		return
	}
	c.rawRelay(name, dc, local, cnt)
}

func (c *Client) counters(name string) *relay.Counters {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats[name]
}

func (c *Client) rawRelay(name string, dc net.Conn, local string, cnt *relay.Counters) {
	lc, err := net.DialTimeout("tcp", local, 5*time.Second)
	if err != nil {
		c.loge("proxy %q: dial local %s: %v", name, local, err)
		_ = dc.Close()
		return
	}
	relay.Pipe(dc, lc, cnt)
}

// httpRelay serves HTTP over the work connection and reverse-proxies each
// request to the local service, preserving the visitor's Host header.
func (c *Client) httpRelay(name string, dc net.Conn, local string, cnt *relay.Counters) {
	target := &url.URL{Scheme: "http", Host: local}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
		},
	}
	sl := &singleListener{ch: make(chan net.Conn, 1), addr: dc.RemoteAddr()}
	sl.ch <- &relay.Conn{Conn: dc, C: cnt, In: true}
	if err := http.Serve(sl, rp); err != nil {
		c.logd("proxy %q: http relay ended: %v", name, err)
	}
	_ = dc.Close()
}

// udpRelay shuffles framed datagrams between the server and per-visitor
// UDP sockets pointed at the local service.
func (c *Client) udpRelay(name string, dc net.Conn, local string, cnt *relay.Counters) {
	var (
		wmu sync.Mutex
		mmu sync.Mutex
		m   = map[string]*net.UDPConn{}
	)
	counted := &relay.Conn{Conn: dc, C: cnt, In: true}

	ensureLocal := func(raddr string) (*net.UDPConn, error) {
		mmu.Lock()
		defer mmu.Unlock()
		if u, ok := m[raddr]; ok {
			return u, nil
		}
		la, err := net.ResolveUDPAddr("udp", local)
		if err != nil {
			return nil, err
		}
		u, err := net.DialUDP("udp", nil, la)
		if err != nil {
			return nil, err
		}
		m[raddr] = u
		go func(raddr string, u *net.UDPConn) {
			buf := make([]byte, 64*1024)
			for {
				n, err := u.Read(buf)
				if err != nil {
					return
				}
				hdr, _ := json.Marshal(map[string]string{"raddr": raddr})
				wmu.Lock()
				err = proto.WritePacket(dc, hdr, buf[:n])
				wmu.Unlock()
				if err != nil {
					return
				}
				cnt.Out.Add(uint64(n))
			}
		}(raddr, u)
		return u, nil
	}

	for {
		hdr, payload, err := proto.ReadPacket(counted)
		if err != nil {
			break
		}
		u, err := ensureLocal(hdr["raddr"].(string))
		if err != nil {
			continue
		}
		if _, err := u.Write(payload); err != nil {
			continue
		}
	}
	mmu.Lock()
	for _, u := range m {
		_ = u.Close()
	}
	mmu.Unlock()
	_ = dc.Close()
}

// singleListener feeds exactly one connection to http.Serve.
type singleListener struct {
	ch   chan net.Conn
	addr net.Addr
}

func (l *singleListener) Accept() (net.Conn, error) {
	c, ok := <-l.ch
	if !ok {
		return nil, fmt.Errorf("listener closed")
	}
	return c, nil
}

func (l *singleListener) Close() error { return nil }
func (l *singleListener) Addr() net.Addr {
	if l.addr != nil {
		return l.addr
	}
	return &net.TCPAddr{}
}

// dialMux opens an attach connection to the server's mux port and
// presents the proxy identity; the server pairs it with an external CONNECT.
func (c *Client) dialMux(name string) (net.Conn, error) {
	c.mu.Lock()
	port := c.muxPort
	c.mu.Unlock()
	if port == 0 {
		return nil, fmt.Errorf("server does not expose a mux port")
	}
	addr := net.JoinHostPort(c.cfg.ServerAddr, strconv.Itoa(port))
	dc, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	cid := proto.RandomHex(16)
	c.mu.Lock()
	c.pending[cid] = nil // placeholder: work pairing happens server-side
	delete(c.pending, cid)
	c.mu.Unlock()
	// The cid for mux pairing is issued by the server-side getWork and is
	// delivered in the work message; dialMux is called with it below.
	return dc, nil
}

// attachMux writes the CONNECT attach request for a server-issued cid.
func (c *Client) attachMux(dc net.Conn, name, cid string) error {
	host := net.JoinHostPort(c.cfg.ServerAddr, strconv.Itoa(c.muxPortNow()))
	req := fmt.Sprintf("CONNECT /vc HTTP/1.1\r\nHost: %s\r\nLumo-Name: %s\r\nLumo-Cid: %s\r\nLumo-Token: %s\r\n\r\n",
		host, name, cid, proto.Sign(c.cfg.AuthToken, []byte(cid)))
	_, err := dc.Write([]byte(req))
	return err
}

func (c *Client) muxPortNow() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.muxPort
}

// runVisitors starts the local listener for every [[visitors]] entry.
func (c *Client) runVisitors(ctx context.Context) {
	for i := range c.cfg.Visitors {
		v := c.cfg.Visitors[i]
		go func(v config.VisitorConfig) {
			var err error
			if v.Type == "udp" {
				err = c.runVisitorUDP(ctx, v)
			} else {
				err = c.runVisitorTCP(ctx, v)
			}
			if err != nil && ctx.Err() == nil {
				c.loge("visitor %q: %v", v.Name, err)
			}
		}(v)
	}
}

// runVisitorTCP exposes a local TCP listener that tunnels through the server
// to an sealed-tcp proxy without any public port on the server.
func (c *Client) runVisitorTCP(ctx context.Context, v config.VisitorConfig) error {
	addr := net.JoinHostPort(v.BindAddr, strconv.Itoa(v.BindPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	c.logi("visitor %q listening on %s -> sealed-tcp %q", v.Name, addr, v.ServerName)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		lc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func(lc net.Conn) {
			dc, err := c.dataConn()
			if err != nil {
				_ = lc.Close()
				return
			}
			if err := proto.Send(dc, proto.Msg{
				"type": "data", "visitor": v.ServerName, "sk": v.SK,
				"vid": proto.RandomHex(8),
			}); err != nil {
				_ = dc.Close()
				_ = lc.Close()
				return
			}
			cnt := c.stats[v.Name]
			relay.Pipe(lc, dc, cnt)
		}(lc)
	}
}

// runVisitorUDP exposes a local UDP listener; each source address gets its
// own tunnel carrying framed datagrams to the sealed-udp proxy's owner.
func (c *Client) runVisitorUDP(ctx context.Context, v config.VisitorConfig) error {
	addr := net.JoinHostPort(v.BindAddr, strconv.Itoa(v.BindPort))
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	ln, err := net.ListenUDP("udp", ua)
	if err != nil {
		return fmt.Errorf("listen udp %s: %w", addr, err)
	}
	c.logi("visitor %q listening on udp %s -> sealed-udp %q", v.Name, addr, v.ServerName)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	buf := make([]byte, 64*1024)
	for {
		n, src, err := ln.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		payload := append([]byte(nil), buf[:n]...)
		go c.visitorUDPPacket(ctx, ln, src, payload, v)
	}
}

// visitorUDPPacket forwards one datagram and relays the reply. A short-lived
// tunnel per datagram keeps the implementation simple and connectionless,
// matching UDP semantics.
func (c *Client) visitorUDPPacket(ctx context.Context, ln *net.UDPConn, src *net.UDPAddr, payload []byte, v config.VisitorConfig) {
	dc, err := c.dataConn()
	if err != nil {
		return
	}
	defer dc.Close()
	if err := proto.Send(dc, proto.Msg{
		"type": "data", "visitor": v.ServerName, "sk": v.SK,
		"vid": proto.RandomHex(8),
	}); err != nil {
		return
	}
	defer func() { _ = dc.SetReadDeadline(time.Now().Add(200 * time.Millisecond)) }()
	if err := proto.WritePacket(dc, []byte(fmt.Sprintf(`{"raddr":%q}`, src.String())), payload); err != nil {
		return
	}
	_ = dc.SetReadDeadline(time.Now().Add(2 * time.Second))
	hdr, resp, err := proto.ReadPacket(dc)
	if err != nil || len(resp) == 0 {
		return
	}
	_ = hdr
	select {
	case <-ctx.Done():
	default:
		_, _ = ln.WriteToUDP(resp, src)
	}
}
