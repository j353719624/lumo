// Package server implements the lumos (tunnel server): client sessions,
// proxy registration, public listeners and traffic relaying.
package server

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/j353719624/lumo/pkg/auth"
	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/log"
	"github.com/j353719624/lumo/pkg/proto"
	"github.com/j353719624/lumo/pkg/relay"
	"github.com/j353719624/lumo/pkg/tlsgen"
	"github.com/j353719624/lumo/pkg/version"
)

// Server is a running lumos instance.
type Server struct {
	cfg   *config.ServerConfig
	start time.Time

	ln       net.Listener
	muxLn net.Listener
	closed   atomic.Bool
	dashSrv  *http.Server

	mu       sync.Mutex
	sessions map[string]*session
	proxies  map[string]*proxyEnt // by globally unique name
	usedPort map[int]bool
	httpDom  map[string]*proxyEnt
	httpsDom map[string]*proxyEnt
	muxDom   map[string]*proxyEnt

	pendMu   sync.Mutex
	pending  map[string]pendEntry
	connsNow atomic.Int64

	webSessions *auth.Sessions
	limiter     *auth.Limiter
	tlsCert     *tls.Certificate
	cfgPath     string
}

type pendEntry struct {
	ch   chan net.Conn
	name string
}

type session struct {
	srv     *Server
	id      string
	since   time.Time
	conn    net.Conn
	wmu     sync.Mutex
	closed  atomic.Bool
	names   map[string]*proxyEnt
	pendReg map[string]chan proto.Msg // name -> reg/unreg reply
	mu      sync.Mutex
}

type proxyEnt struct {
	name       string
	kind       string
	sk         string
	remotePort int
	domains    []string
	owner      *session
	since      time.Time

	ln    net.Listener // tcp
	udpLn *net.UDPConn // udp

	workCh chan net.Conn

	dead atomic.Bool
	wmu  sync.Mutex
	data net.Conn // current udp data connection

	cnt relay.Counters
}

// New creates a Server from validated configuration.
func New(cfg *config.ServerConfig) *Server {
	s := &Server{
		cfg:         cfg,
		start:       time.Now(),
		sessions:    map[string]*session{},
		proxies:     map[string]*proxyEnt{},
		usedPort:    map[int]bool{},
		httpDom:     map[string]*proxyEnt{},
		httpsDom:    map[string]*proxyEnt{},
		muxDom:      map[string]*proxyEnt{},
		pending:     map[string]pendEntry{},
		webSessions: auth.NewSessions(7 * 24 * time.Hour),
		limiter:     auth.NewLimiter(10 * time.Minute),
		cfgPath:     cfg.ConfigPath,
	}
	// Generate self-signed TLS certificate for transport encryption.
	if cert, err := tlsgen.SelfSigned(); err == nil {
		s.tlsCert = &cert
	}
	return s
}

// Run blocks until ctx is cancelled or the listener fails.
func (s *Server) Run(ctx context.Context) error {
	if s.cfg.MuxPort > 0 {
		go s.runMux()
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.cfg.BindPort))
	if err != nil {
		return fmt.Errorf("bind port %d: %w", s.cfg.BindPort, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.usedPort[ln.Addr().(*net.TCPAddr).Port] = true
	s.mu.Unlock()
	log.Infof("lumos %s listening on :%d", version.Version, s.cfg.BindPort)

	if s.cfg.VhostHTTPPort > 0 {
		go s.runVhost("http")
	}
	if s.cfg.VhostHTTPSPort > 0 {
		go s.runVhost("https")
	}
	if s.cfg.Web.Port > 0 {
		go s.runDash()
	}
	go func() {
		<-ctx.Done()
		s.Close()
	}()
	go s.acceptLoop()
	<-ctx.Done()
	return nil
}

// acceptLoop serves the tunnel listener; the listener pointer may be swapped
// by rebind while the loop runs.
func (s *Server) acceptLoop() {
	for {
		if s.closed.Load() {
			return
		}
		s.mu.Lock()
		ln := s.ln
		s.mu.Unlock()
		if ln == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		c, err := ln.Accept()
		if err != nil {
			if s.closed.Load() {
				return
			}
			// 监听被热切换关闭：短暂等待后使用新监听继续
			time.Sleep(150 * time.Millisecond)
			continue
		}
		go s.handleConn(c)
	}
}

// Rebind switches the tunnel listener to a new port without restarting the
// process. Existing sessions keep running; new clients must use the new port.
func (s *Server) Rebind(port int) error {
	nl, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("port %d: %w", port, err)
	}
	s.mu.Lock()
	old := s.ln
	oldPort := 0
	if old != nil {
		if ta, ok := old.Addr().(*net.TCPAddr); ok {
			oldPort = ta.Port
		}
	}
	s.ln = nl
	s.usedPort[port] = true
	if oldPort != 0 {
		delete(s.usedPort, oldPort)
	}
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	log.Infof("tunnel listener moved to :%d", port)
	return nil
}

// Close shuts everything down.
func (s *Server) Close() {
	if s.closed.Swap(true) {
		return
	}
	_ = s.ln.Close()
	if s.dashSrv != nil {
		_ = s.dashSrv.Close()
	}
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, ses := range s.sessions {
		sessions = append(sessions, ses)
	}
	s.mu.Unlock()
	for _, ses := range sessions {
		ses.drop("server shutting down")
	}
}

// DropAllSessions closes every client session, forcing clients to reconnect
// with the current configuration (used after server-side setting changes).
func (s *Server) DropAllSessions(reason string) {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, ses := range s.sessions {
		sessions = append(sessions, ses)
	}
	s.mu.Unlock()
	for _, ses := range sessions {
		ses.drop(reason)
	}
}

func (s *Server) handleConn(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))

	// 自动检测 TLS：首字节 0x16 = TLS ClientHello
	tlsBuf := make([]byte, 1)
	if _, err := io.ReadFull(c, tlsBuf); err != nil {
		_ = c.Close()
		return
	}
	if tlsBuf[0] == 0x16 && s.tlsCert != nil {
		// 探测字节必须先还给连接，否则 TLS 记录从第二字节开始解析，
		// 握手必然失败（表现为客户端 connection reset）
		tlsConn := tls.Server(&bufferedConn{Conn: c, pre: tlsBuf}, &tls.Config{Certificates: []tls.Certificate{*s.tlsCert}})
		if err := tlsConn.Handshake(); err != nil {
			_ = c.Close()
			return
		}
		c = tlsConn
	} else {
		c = &bufferedConn{Conn: c, pre: tlsBuf}
	}

	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	first, err := proto.Recv(c)
	if err != nil {
		_ = c.Close()
		return
	}
	switch first.Str("type") {
	case "hello":
		s.newSession(c, first)
	case "data":
		s.handleData(c, first)
	default:
		log.Warnf("rejecting connection: unexpected first message %q", first.Str("type"))
		_ = proto.Send(c, proto.Msg{"type": "error", "message": "expected hello or data"})
		_ = c.Close()
	}
}

func (s *Server) newSession(c net.Conn, hello proto.Msg) {
	nonce, err := hex.DecodeString(hello.Str("nonce"))
	if err != nil || len(nonce) == 0 {
		_ = proto.Send(c, proto.Msg{"type": "error", "message": "bad nonce"})
		_ = c.Close()
		return
	}
	expect := proto.Sign(s.cfg.AuthToken, nonce)
	if !hmac.Equal([]byte(expect), []byte(hello.Str("auth"))) {
		log.Warnf("client %q failed authentication", hello.Str("client"))
		_ = proto.Send(c, proto.Msg{"type": "error", "message": "authentication failed"})
		_ = c.Close()
		return
	}
	_ = c.SetDeadline(time.Time{})
	sid := hello.Str("client")
	if sid == "" {
		sid = proto.RandomHex(8)
	}
	ses := &session{
		srv:     s,
		id:      sid,
		since:   time.Now(),
		conn:    c,
		names:   map[string]*proxyEnt{},
		pendReg: map[string]chan proto.Msg{},
	}
	s.mu.Lock()
	if _, dup := s.sessions[sid]; dup {
		s.mu.Unlock()
		_ = proto.Send(c, proto.Msg{"type": "error", "message": "client id already connected"})
		_ = c.Close()
		return
	}
	s.sessions[sid] = ses
	s.mu.Unlock()
	log.Infof("client %q connected (session %s)", sid, sid[:6])

	if err := ses.send(proto.Msg{"type": "welcome", "sid": sid, "version": version.Version, "mux_port": s.cfg.MuxPort}); err != nil {
		ses.drop("write welcome: " + err.Error())
		return
	}
	ses.readLoop()
}

func (s *session) send(m proto.Msg) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return proto.Send(s.conn, m)
}

func (s *session) readLoop() {
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		m, err := proto.Recv(s.conn)
		if err != nil {
			if !s.closed.Load() {
				s.drop("read: " + err.Error())
			}
			return
		}
		switch m.Str("type") {
		case "ping":
			if err := s.send(proto.Msg{"type": "pong"}); err != nil {
				s.drop("write pong: " + err.Error())
				return
			}
		case "reg":
			s.srv.handleReg(s, m)
		case "unreg":
			s.srv.handleUnreg(s, m)
		default:
			log.Debugf("session %s: ignoring message type %q", s.id, m.Str("type"))
		}
	}
}

// drop tears the session down and releases every proxy it owns.
func (s *session) drop(reason string) {
	if s.closed.Swap(true) {
		return
	}
	log.Infof("client %q disconnected: %s", s.id, reason)
	s.srv.mu.Lock()
	for name, ent := range s.names {
		s.srv.removeProxyLocked(name, ent)
	}
	delete(s.srv.sessions, s.id)
	s.srv.mu.Unlock()
	_ = s.conn.Close()
}

func (s *Server) handleData(c net.Conn, m proto.Msg) {
	// sealed-tcp/sealed-udp visitor data connections carry the visitor marker instead of
	// a cid; pair them with a work connection from the proxy owner.
	if v := m.Str("visitor"); v != "" {
		s.handleVisitorData(c, v, m.Str("sk"))
		return
	}
	cid := m.Str("cid")
	s.pendMu.Lock()
	pe, ok := s.pending[cid]
	if ok {
		delete(s.pending, cid)
	}
	s.pendMu.Unlock()
	if !ok || pe.name != m.Str("name") {
		log.Warnf("data connection with unknown cid rejected")
		_ = c.Close()
		return
	}
	pe.ch <- c
}

// handleVisitorData pairs an sealed-tcp/sealed-udp visitor data connection with a fresh
// work connection from the proxy owner, then pipes payload both ways. The
// two sides speak the same framing end to end, so the server only copies.
func (s *Server) handleVisitorData(c net.Conn, name, sk string) {
	s.mu.Lock()
	ent := s.proxies[name]
	s.mu.Unlock()
	if ent == nil || (ent.kind != "sealed-tcp" && ent.kind != "sealed-udp") {
		log.Warnf("visitor data for unknown proxy %q rejected", name)
		_ = c.Close()
		return
	}
	if ent.sk == "" || sk != ent.sk {
		log.Warnf("visitor data for proxy %q rejected: bad secret key", name)
		_ = c.Close()
		return
	}
	owner, err := s.getWork(ent, 10*time.Second)
	if err != nil {
		log.Warnf("visitor data for proxy %q: %v", name, err)
		_ = c.Close()
		return
	}
	log.Infof("proxy %q (%s): visitor channel established", name, ent.kind)
	relay.Pipe(c, owner, &ent.cnt)
}

// runMux serves the mux entry port: clients ATTACH with Lumo-Name and
// are later paired with external CONNECT requests routed by Host.
func (s *Server) runMux() {
	addr := fmt.Sprintf(":%d", s.cfg.MuxPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Errorf("mux listen %s: %v", addr, err)
		return
	}
	s.mu.Lock()
	s.muxLn = ln
	s.mu.Unlock()
	log.Infof("mux listening on %s", addr)
	for {
		c, err := ln.Accept()
		if err != nil {
			if s.closed.Load() {
				return
			}
			continue
		}
		go s.serveMuxConn(c)
	}
}

// serveMuxConn handles one connection on the mux entry port. Clients
// attach with Lumo-Name headers (verified against the auth token); external
// callers issue CONNECT and are routed by Host to the matching proxy.
func (s *Server) serveMuxConn(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		_ = c.Close()
		return
	}
	_ = c.SetDeadline(time.Time{})
	if name := req.Header.Get("Lumo-Name"); name != "" {
		// client attach: verify token + proxy, then hand off to the pairing
		cid := req.Header.Get("Lumo-Cid")
		if !hmac.Equal([]byte(proto.Sign(s.cfg.AuthToken, []byte(cid))),
			[]byte(req.Header.Get("Lumo-Token"))) {
			_ = c.Close()
			return
		}
		s.mu.Lock()
		ent := s.proxies[name]
		s.mu.Unlock()
		if ent == nil || ent.kind != "mux" {
			_ = c.Close()
			return
		}
		s.pendMu.Lock()
		pe, ok := s.pending[cid]
		s.pendMu.Unlock()
		if !ok || pe.name != name {
			_ = c.Close()
			return
		}
		pe.ch <- c
		return
	}
	// external CONNECT: route by Host header (domain) or CONNECT target
	host := strings.ToLower(strings.TrimSpace(req.Host))
	if host == "" {
		host = strings.ToLower(req.URL.Host)
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	s.mu.Lock()
	ent := s.muxDom[host]
	if ent == nil {
		ent = s.proxies[host]
	}
	s.mu.Unlock()
	if ent == nil || ent.kind != "mux" {
		_ = c.Close()
		return
	}
	owner, err := s.getWork(ent, 10*time.Second)
	if err != nil {
		_ = c.Close()
		return
	}
	if _, err := c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = owner.Close()
		_ = c.Close()
		return
	}
	relay.Pipe(c, owner, &ent.cnt)
}

func (s *Server) getWork(ent *proxyEnt, timeout time.Duration) (net.Conn, error) {
	select {
	case c := <-ent.workCh:
		return c, nil
	default:
	}
	if ent.dead.Load() || ent.owner.closed.Load() {
		return nil, fmt.Errorf("proxy %q is offline", ent.name)
	}
	cid := proto.RandomHex(16)
	ch := make(chan net.Conn, 1)
	s.pendMu.Lock()
	s.pending[cid] = pendEntry{ch: ch, name: ent.name}
	s.pendMu.Unlock()
	defer func() {
		s.pendMu.Lock()
		delete(s.pending, cid)
		s.pendMu.Unlock()
	}()
	if err := ent.owner.send(proto.Msg{"type": "work", "name": ent.name, "cid": cid}); err != nil {
		return nil, fmt.Errorf("request work connection: %w", err)
	}
	select {
	case c := <-ch:
		return c, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timed out waiting for work connection")
	}
}

// removeProxyLocked tears down one proxy. Callers must hold s.mu.
func (s *Server) removeProxyLocked(name string, ent *proxyEnt) {
	ent.dead.Store(true)
	if ent.ln != nil {
		_ = ent.ln.Close()
	}
	if ent.udpLn != nil {
		_ = ent.udpLn.Close()
	}
	ent.wmu.Lock()
	if ent.data != nil {
		_ = ent.data.Close()
	}
	ent.wmu.Unlock()
	switch ent.kind {
	case "http":
		for _, d := range ent.domains {
			delete(s.httpDom, d)
		}
	case "https":
		for _, d := range ent.domains {
			delete(s.httpsDom, d)
		}
	case "mux":
		for _, d := range ent.domains {
			delete(s.muxDom, d)
		}
	case "tcp", "udp":
		delete(s.usedPort, ent.remotePort)
	}
	delete(s.proxies, name)
	log.Infof("proxy %q (%s) removed", name, ent.kind)
}
