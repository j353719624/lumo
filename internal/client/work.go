package client

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"time"

	"lumo/internal/log"
	"lumo/internal/proto"
	"lumo/internal/relay"
)

// handleWork fulfils a work-connection request by dialing the server and
// handing the fresh connection to the proxy's relay implementation.
func (c *Client) handleWork(m proto.Msg) {
	name := m.Str("name")
	c.mu.Lock()
	p := c.proxies[name]
	c.mu.Unlock()
	if p == nil {
		log.Warnf("work request for unknown proxy %q", name)
		return
	}
	dc, err := net.DialTimeout("tcp", c.addr(), 10*time.Second)
	if err != nil {
		log.Errorf("proxy %q: dial for work connection: %v", name, err)
		return
	}
	if err := proto.Send(dc, proto.Msg{"type": "data", "name": name, "cid": m.Str("cid")}); err != nil {
		log.Errorf("proxy %q: announce data connection: %v", name, err)
		_ = dc.Close()
		return
	}
	local := net.JoinHostPort(p.LocalIP, strconv.Itoa(p.LocalPort))
	cnt := c.counters(name)
	switch p.Type {
	case "tcp", "https":
		c.rawRelay(name, dc, local, cnt)
	case "http":
		c.httpRelay(name, dc, local, cnt)
	case "udp":
		c.udpRelay(name, dc, local, cnt)
	}
}

func (c *Client) counters(name string) *relay.Counters {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats[name]
}

func (c *Client) rawRelay(name string, dc net.Conn, local string, cnt *relay.Counters) {
	lc, err := net.DialTimeout("tcp", local, 5*time.Second)
	if err != nil {
		log.Errorf("proxy %q: dial local %s: %v", name, local, err)
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
		log.Debugf("proxy %q: http relay ended: %v", name, err)
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
