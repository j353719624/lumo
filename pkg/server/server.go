// Package server implements the lumos (tunnel server): client sessions,
// proxy registration, public listeners and traffic relaying.
package server

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
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
	"github.com/j353719624/lumo/pkg/token"
	"github.com/j353719624/lumo/pkg/udpstream"
	"github.com/j353719624/lumo/pkg/version"
)

// Server is a running lumos instance.
type Server struct {
	cfg   *config.ServerConfig
	start time.Time

	ln    net.Listener
	muxLn net.Listener
	// kcpLn hosts KCP (UDP) client sessions when kcpPort is configured.
	kcpLn   *udpstream.Listener
	closed  atomic.Bool
	dashSrv *http.Server

	mu       sync.Mutex
	sessions map[string]*session
	proxies  map[string]*proxyEnt // by globally unique name
	usedPort map[int]bool
	httpDom  map[string][]*proxyEnt
	httpsDom map[string][]*proxyEnt
	muxDom   map[string]*proxyEnt

	pendMu  sync.Mutex
	pending map[string]pendEntry

	// proxyMem remembers recently-seen proxies after their client went
	// away, so the console can show them greyed out as offline.
	proxyMem map[string]*proxyMemEntry
	connsNow atomic.Int64

	webSessions *auth.Sessions
	limiter     *auth.Limiter
	tlsCert     *tls.Certificate
	cfgPath     string
	webBase     string // URL prefix when deployed behind a gateway (/app/lumo-server)
	webSocket   string // unix socket for app-gateway desktop entries
}

// SetWebBase puts the console under a URL prefix (gateway-style deployments).
func (s *Server) SetWebBase(base string) { s.webBase = strings.TrimSpace(base) }

// SetWebSocket makes the console also listen on a unix socket.
func (s *Server) SetWebSocket(path string) { s.webSocket = strings.TrimSpace(path) }

type proxyMemEntry struct {
	name       string
	label      string
	clientName string
	kind       string
	entry      string
	trafficIn  uint64
	trafficOut uint64
	since      time.Time
	removed    time.Time
}

const proxyMemTTL = 7 * 24 * time.Hour

type pendEntry struct {
	ch   chan net.Conn
	name string
}

// display returns the client's display name when it provided one, else the id.
func (ses *session) display() string {
	if ses.name != "" {
		return ses.name
	}
	return ses.id
}

type session struct {
	srv     *Server
	id      string
	name    string
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
	label      string // client-provided display name for this mapping
	kind       string
	visitor    bool // 访客型隧道：只凭密钥接入，服务端不占公网端口
	sk         string
	remotePort int
	domains    []string
	subPaths   []string
	httpUser   string
	httpPass   string
	punchAddr  string
	localPort  int // 原始服务端口（sealed-tcp 供访客用相同端口）
	owner      *session
	since      time.Time

	ln    net.Listener // tcp
	udpLn *net.UDPConn // udp

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
		httpDom:     map[string][]*proxyEnt{},
		proxyMem:    map[string]*proxyMemEntry{},
		httpsDom:    map[string][]*proxyEnt{},
		muxDom:      map[string]*proxyEnt{},
		pending:     map[string]pendEntry{},
		webSessions: auth.NewSessions(5 * time.Minute), // 滑动过期：有操作就续期，闲置 5 分钟退出
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
	if s.cfg.KCPPort > 0 {
		if err := s.StartKCP(s.cfg.KCPPort); err != nil {
			return fmt.Errorf("kcp port %d: %w", s.cfg.KCPPort, err)
		}
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
	// 启动时确保所有配置的公网端口都已放行（热更新端口、机器重启后同样生效）
	go func() {
		openFirewallPort(s.cfg.BindPort, "tcp")
		if s.cfg.KCPPort > 0 {
			openFirewallPort(s.cfg.KCPPort, "udp")
		}
		if s.cfg.VhostHTTPPort > 0 {
			openFirewallPort(s.cfg.VhostHTTPPort, "tcp")
		}
		if s.cfg.VhostHTTPSPort > 0 {
			openFirewallPort(s.cfg.VhostHTTPSPort, "tcp")
		}
		if s.cfg.MuxPort > 0 {
			openFirewallPort(s.cfg.MuxPort, "tcp")
		}
	}()

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

// StartKCP brings up (or re-binds) the UDP tunnel listener. Port 0 stops it.
func (s *Server) StartKCP(port int) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("port %d out of range", port)
	}
	s.mu.Lock()
	old := s.kcpLn
	s.kcpLn = nil
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	if port == 0 {
		log.Infof("kcp transport stopped")
		return nil
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		return err
	}
	l := udpstream.NewListener(pc)
	s.mu.Lock()
	s.kcpLn = l
	s.mu.Unlock()
	go s.kcpAcceptLoop(l)
	log.Infof("kcp transport listening on udp :%d", port)
	return nil
}

// kcpAcceptLoop accepts client sessions; every multiplexed stream is treated
// like an incoming TCP connection (first message disambiguates control/data).
func (s *Server) kcpAcceptLoop(l *udpstream.Listener) {
	for {
		sess, err := l.AcceptSession()
		if err != nil {
			return
		}
		go func(us *udpstream.Session) {
			for {
				st, err := us.Accept()
				if err != nil {
					return
				}
				go s.handleConn(st)
			}
		}(sess)
	}
}

// Close shuts everything down.
func (s *Server) Close() {
	if s.closed.Swap(true) {
		return
	}
	_ = s.ln.Close()
	s.mu.Lock()
	kcpLn := s.kcpLn
	s.kcpLn = nil
	s.mu.Unlock()
	if kcpLn != nil {
		_ = kcpLn.Close()
	}
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
	if s.cfg.AuthPubKey != "" {
		// 公钥模式：验证 Ed25519 签名（服务端不保存令牌本身）
		rcv := hello.Str("authp")
		if !token.VerifyProof(s.cfg.AuthPubKey, nonce, rcv) {
			who := hello.Str("client")
			if n := hello.Str("name"); n != "" {
				who = n + " (" + who + ")"
			}
			log.Warnf("client %q failed authentication (pubkey mode)", who)
			_ = proto.Send(c, proto.Msg{"type": "error", "message": "authentication failed"})
			_ = c.Close()
			return
		}
	} else {
		expect := proto.Sign(s.cfg.AuthToken, nonce)
		if !hmac.Equal([]byte(expect), []byte(hello.Str("auth"))) {
			who := hello.Str("client")
			if n := hello.Str("name"); n != "" {
				who = n + " (" + who + ")"
			}
			log.Warnf("client %q failed authentication", who)
			_ = proto.Send(c, proto.Msg{"type": "error", "message": "authentication failed"})
			_ = c.Close()
			return
		}
	}
	_ = c.SetDeadline(time.Time{})
	sid := hello.Str("client")
	if sid == "" {
		sid = proto.RandomHex(8)
	}
	ses := &session{
		srv:     s,
		id:      sid,
		name:    hello.Str("name"),
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
	name := hello.Str("name")
	if name == "" {
		name = sid
	}
	log.Infof("client %q connected (session %s)", name, sid[:6])

	if err := ses.send(proto.Msg{"type": "welcome", "sid": sid, "version": version.Version, "mux_port": s.cfg.MuxPort, "kcp_port": s.cfg.KCPPort}); err != nil {
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
		case "list-services":
			// 访问端自动发现：列出其它会话注册的 sealed-tcp/sealed-udp 服务（含 sk，
			// 能通过令牌认证的会话视为可信）
			items := []map[string]any{}
			s.srv.mu.Lock()
			for n, ent := range s.srv.proxies {
				if ent.owner == s {
					continue
				}
				if ent.kind == "sealed-tcp" || ent.kind == "sealed-udp" {
					items = append(items, map[string]any{"name": n, "label": ent.label, "sk": ent.sk, "type": ent.kind, "port": ent.remotePort})
				}
			}
			s.srv.mu.Unlock()
			_ = proto.Send(s.conn, proto.Msg{"type": "services", "services": items})

		case "punch_addr":
			s.srv.handlePunchAddr(s, m)
		case "punch_punch":
			s.srv.handlePunchPunch(s, m)
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
	log.Infof("client %q disconnected: %s", s.display(), log.StripAddrs(reason))
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
	if v, _ := m["compress"].(bool); v {
		c = relay.CompressConn(c)
	}
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
	if ent == nil {
		// 显示标签（如 "飞牛"）也能定位：按技术名查不到时回退到 label
		for _, e := range s.proxies {
			if e.label == name {
				ent = e
				break
			}
		}
	}
	s.mu.Unlock()
	if ent == nil || !ent.visitor {
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
		if s.cfg.AuthPubKey != "" {
			if !token.VerifyProof(s.cfg.AuthPubKey, []byte(cid), req.Header.Get("Lumo-TokenP")) {
				_ = c.Close()
				return
			}
		} else if !hmac.Equal([]byte(proto.Sign(s.cfg.AuthToken, []byte(cid))),
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
			s.httpDom[d] = withoutEnt(s.httpDom[d], ent)
			if len(s.httpDom[d]) == 0 {
				delete(s.httpDom, d)
			}
		}
	case "https":
		for _, d := range ent.domains {
			s.httpsDom[d] = withoutEnt(s.httpsDom[d], ent)
			if len(s.httpsDom[d]) == 0 {
				delete(s.httpsDom, d)
			}
		}
	case "mux":
		for _, d := range ent.domains {
			delete(s.muxDom, d)
		}
	case "tcp", "udp":
		delete(s.usedPort, ent.remotePort)
	}
	delete(s.proxies, name)
	if s.proxyMem != nil {
		label := ent.name
		if ent.owner != nil && ent.owner.name != "" {
			label = ent.owner.name
		}
		if ent.label != "" {
			if ent.owner != nil && ent.owner.name != "" {
				label = ent.owner.name + " · " + ent.label
			} else {
				label = ent.label
			}
		}
		entry := ""
		switch ent.kind {
		case "tcp", "udp":
			entry = fmt.Sprintf(":%d", ent.remotePort)
		case "http", "https", "mux":
			if len(ent.domains) > 0 {
				entry = ent.domains[0]
			}
		}
		s.proxyMem[name] = &proxyMemEntry{
			name:       name,
			label:      label,
			clientName: ent.ownerNameSafe(),
			kind:       ent.kind,
			entry:      entry,
			trafficIn:  ent.cnt.In.Load(),
			trafficOut: ent.cnt.Out.Load(),
			since:      ent.since,
			removed:    time.Now(),
		}
	}
	log.Infof("proxy %q (%s) removed", name, ent.kind)
}

// ownerNameSafe returns the client display name, tolerating a nil owner.
func (e *proxyEnt) ownerNameSafe() string {
	if e.owner != nil {
		return e.owner.name
	}
	return ""
}

// withoutEnt returns list without ent (nil when absent).
func withoutEnt(list []*proxyEnt, ent *proxyEnt) []*proxyEnt {
	out := list[:0]
	for _, e := range list {
		if e != ent {
			out = append(out, e)
		}
	}
	return out
}

// handlePunchAddr records the reflexive UDP address a service client punched
// out for its punch proxy.
func (s *Server) handlePunchAddr(ses *session, m proto.Msg) {
	name := m.Str("name")
	addr := m.Str("addr")
	s.mu.Lock()
	ent := s.proxies[name]
	if ent != nil && ent.owner == ses && ent.kind == "punch" {
		ent.punchAddr = addr
		log.Infof("punch %q: punch address %s", name, log.MaskAddr(addr))
	}
	s.mu.Unlock()
}

// handlePunchPunch pairs a visitor with an punch service client: both sides get
// the other's punched address plus a shared session number, then punch.
func (s *Server) handlePunchPunch(ses *session, m proto.Msg) {
	name := m.Str("name")
	sk := m.Str("sk")
	visitorAddr := m.Str("addr")
	s.mu.Lock()
	ent := s.proxies[name]
	ok := ent != nil && ent.kind == "punch" && ent.sk != "" && ent.sk == sk && ent.punchAddr != "" && !ent.dead.Load()
	var svc *session
	if ok {
		svc = ent.owner
	}
	peerAddr := ""
	if ok {
		peerAddr = ent.punchAddr
	}
	s.mu.Unlock()
	if !ok {
		log.Warnf("punch %q: punch refused (service side not ready)", name)
		_ = ses.send(proto.Msg{"type": "punch_fail", "name": name, "error": "service side not ready"})
		return
	}
	log.Infof("punch %q: pairing visitor %s with service %s", name, log.MaskAddr(visitorAddr), log.MaskAddr(peerAddr))
	conv := randomConv()
	if err := svc.send(proto.Msg{"type": "punch_peer", "name": name, "conv": conv, "addr": visitorAddr}); err != nil {
		_ = ses.send(proto.Msg{"type": "punch_fail", "name": name, "error": "service side unreachable"})
		return
	}
	_ = ses.send(proto.Msg{"type": "punch_peer", "name": name, "conv": conv, "addr": peerAddr})
}

// randomConv returns a non-zero session number for a punched punch session.
func randomConv() int {
	var b [4]byte
	_, _ = rand.Read(b[:])
	v := int(binary.BigEndian.Uint32(b[:]))
	if v == 0 {
		v = 1
	}
	return v
}
