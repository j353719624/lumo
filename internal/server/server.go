// Package server implements the lumos (tunnel server): client sessions,
// proxy registration, public listeners and traffic relaying.
package server

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"fmt"
	"lumo/internal/auth"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"lumo/internal/config"
	"lumo/internal/log"
	"lumo/internal/proto"
	"lumo/internal/relay"
	"lumo/internal/version"
)

// Server is a running lumos instance.
type Server struct {
	cfg   *config.ServerConfig
	start time.Time

	ln      net.Listener
	closed  atomic.Bool
	dashSrv *http.Server

	mu       sync.Mutex
	sessions map[string]*session
	proxies  map[string]*proxyEnt // by globally unique name
	usedPort map[int]bool
	httpDom  map[string]*proxyEnt
	httpsDom map[string]*proxyEnt

	pendMu   sync.Mutex
	pending  map[string]pendEntry
	connsNow atomic.Int64

	webSessions *auth.Sessions
	limiter     *auth.Limiter
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
	return &Server{
		cfg:         cfg,
		start:       time.Now(),
		sessions:    map[string]*session{},
		proxies:     map[string]*proxyEnt{},
		usedPort:    map[int]bool{},
		httpDom:     map[string]*proxyEnt{},
		httpsDom:    map[string]*proxyEnt{},
		pending:     map[string]pendEntry{},
		webSessions: auth.NewSessions(7 * 24 * time.Hour),
		limiter:     auth.NewLimiter(10 * time.Minute),
	}
}

// Run blocks until ctx is cancelled or the listener fails.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.cfg.BindPort))
	if err != nil {
		return fmt.Errorf("bind port %d: %w", s.cfg.BindPort, err)
	}
	s.ln = ln
	s.usedPort[ln.Addr().(*net.TCPAddr).Port] = true
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
	for {
		c, err := ln.Accept()
		if err != nil {
			if s.closed.Load() {
				return nil
			}
			return err
		}
		go s.handleConn(c)
	}
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

func (s *Server) handleConn(c net.Conn) {
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

	if err := ses.send(proto.Msg{"type": "welcome", "sid": sid, "version": version.Version}); err != nil {
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
	case "tcp", "udp":
		delete(s.usedPort, ent.remotePort)
	}
	delete(s.proxies, name)
	log.Infof("proxy %q (%s) removed", name, ent.kind)
}
