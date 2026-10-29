package server

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/j353719624/lumo/pkg/log"
	"github.com/j353719624/lumo/pkg/proto"
	"github.com/j353719624/lumo/pkg/relay"
)

// handleReg processes a proxy registration from a client session.
func (s *Server) handleReg(ses *session, m proto.Msg) {
	reply := func(ok bool, message string) {
		t := "reg_ok"
		if !ok {
			t = "reg_err"
		}
		_ = ses.send(proto.Msg{"type": t, "name": m.Str("name"), "message": message})
	}

	name := m.Str("name")
	kind := m.Str("kind")
	if name == "" || len(name) > 128 {
		reply(false, "invalid proxy name")
		return
	}
	switch kind {
	case "tcp", "udp", "http", "https", "mux", "sealed-tcp", "sealed-udp":
	default:
		reply(false, "unsupported proxy kind "+kind)
		return
	}

	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		reply(false, "server shutting down")
		return
	}
	if _, exists := s.proxies[name]; exists {
		s.mu.Unlock()
		reply(false, "proxy name already in use")
		return
	}
	ent := &proxyEnt{
		name:       name,
		label:      m.Str("label"),
		kind:       kind,
		sk:         m.Str("sk"),
		remotePort: m.Int("remote_port"),
		domains:    lowerAll(m.StrList("domains")),
		owner:      ses,
		since:      time.Now(),
		workCh:     make(chan net.Conn, 64),
	}
	if err := s.setupProxyLocked(ent); err != nil {
		s.mu.Unlock()
		reply(false, err.Error())
		return
	}
	s.proxies[name] = ent
	ses.mu.Lock()
	ses.names[name] = ent
	ses.mu.Unlock()
	s.mu.Unlock()

	if ent.label != "" {
		log.Infof("proxy %q (%s, label %q) registered by client %q", name, kind, ent.label, ses.id)
	} else {
		log.Infof("proxy %q (%s) registered by client %q", name, kind, ses.id)
	}
	reply(true, "")

	// 新实例带来的公网端口自动放行，客户端侧零配置
	if ent.kind == "tcp" || ent.kind == "udp" {
		go openFirewallPort(ent.remotePort, ent.kind)
	}

	switch ent.kind {
	case "tcp":
		go s.acceptTCP(ent)
	case "udp":
		go s.relayUDP(ent)
	}
}

// setupProxyLocked opens listeners and claims names/ports. Callers hold s.mu.
func (s *Server) setupProxyLocked(ent *proxyEnt) error {
	switch ent.kind {
	case "tcp":
		if ent.remotePort < 1 || ent.remotePort > 65535 {
			return fmt.Errorf("remote_port %d out of range", ent.remotePort)
		}
		if s.usedPort[ent.remotePort] {
			return fmt.Errorf("port %d already in use", ent.remotePort)
		}
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", ent.remotePort))
		if err != nil {
			return fmt.Errorf("listen tcp :%d: %w", ent.remotePort, err)
		}
		s.usedPort[ent.remotePort] = true
		ent.ln = ln
	case "udp":
		if ent.remotePort < 1 || ent.remotePort > 65535 {
			return fmt.Errorf("remote_port %d out of range", ent.remotePort)
		}
		if s.usedPort[ent.remotePort] {
			return fmt.Errorf("port %d already in use", ent.remotePort)
		}
		ln, err := net.ListenUDP("udp", &net.UDPAddr{Port: ent.remotePort})
		if err != nil {
			return fmt.Errorf("listen udp :%d: %w", ent.remotePort, err)
		}
		s.usedPort[ent.remotePort] = true
		ent.udpLn = ln
	case "sealed-tcp", "sealed-udp":
		if ent.sk == "" {
			return fmt.Errorf("sk is required for %s proxies", ent.kind)
		}
	case "mux":
		if s.cfg.MuxPort == 0 {
			return fmt.Errorf("server has no mux port configured")
		}
		if len(ent.domains) == 0 {
			return fmt.Errorf("at least one domain is required")
		}
		for _, d := range ent.domains {
			if _, taken := s.muxDom[d]; taken {
				return fmt.Errorf("domain %q already in use", d)
			}
		}
		for _, d := range ent.domains {
			s.muxDom[d] = ent
		}
	case "http", "https":
		if (ent.kind == "http" && s.cfg.VhostHTTPPort == 0) ||
			(ent.kind == "https" && s.cfg.VhostHTTPSPort == 0) {
			return fmt.Errorf("server has no vhost port for %s proxies", ent.kind)
		}
		if len(ent.domains) == 0 {
			return fmt.Errorf("at least one domain is required")
		}
		table := s.httpDom
		if ent.kind == "https" {
			table = s.httpsDom
		}
		for _, d := range ent.domains {
			if _, taken := table[d]; taken {
				return fmt.Errorf("domain %q already in use", d)
			}
		}
		for _, d := range ent.domains {
			table[d] = ent
		}
	}
	return nil
}

// handleUnreg removes a proxy owned by the requesting session.
func (s *Server) handleUnreg(ses *session, m proto.Msg) {
	name := m.Str("name")
	s.mu.Lock()
	ent, ok := s.proxies[name]
	if ok && ent.owner != ses {
		ok = false
	}
	if ok {
		s.removeProxyLocked(name, ent)
	}
	s.mu.Unlock()
	t := "unreg_ok"
	if !ok {
		t = "unreg_err"
	}
	_ = ses.send(proto.Msg{"type": t, "name": name})
}

// acceptTCP loops on a tcp proxy's public listener and splices every
// visitor to a fresh work connection.
func (s *Server) acceptTCP(ent *proxyEnt) {
	for {
		vis, err := ent.ln.Accept()
		if ent.dead.Load() || err != nil {
			return
		}
		go s.serveTCP(ent, vis)
	}
}

func (s *Server) serveTCP(ent *proxyEnt, vis net.Conn) {
	defer vis.Close()
	s.connsNow.Add(1)
	defer s.connsNow.Add(-1)
	wc, err := s.getWork(ent, 10*time.Second)
	if err != nil {
		log.Debugf("tcp proxy %q: %v", ent.name, err)
		return
	}
	relay.Pipe(vis, wc, &ent.cnt)
}

// relayUDP moves datagrams between the public UDP socket and one framed
// data connection per proxy. raddr travels in the packet header so replies
// find their way back to the right visitor.
func (s *Server) relayUDP(ent *proxyEnt) {
	buf := make([]byte, 64*1024)
	for {
		n, raddr, err := ent.udpLn.ReadFromUDP(buf)
		if ent.dead.Load() || err != nil {
			return
		}
		dc := s.udpDataConn(ent)
		if dc == nil {
			continue
		}
		hdr, _ := json.Marshal(map[string]string{"raddr": raddr.String()})
		ent.wmu.Lock()
		err = proto.WritePacket(dc, hdr, buf[:n])
		ent.wmu.Unlock()
		if err != nil {
			s.dropUDPData(ent)
			continue
		}
		ent.cnt.Out.Add(uint64(n))
	}
}

// udpDataConn returns the live data connection, establishing one if needed.
func (s *Server) udpDataConn(ent *proxyEnt) net.Conn {
	ent.wmu.Lock()
	defer ent.wmu.Unlock()
	if ent.data != nil {
		return ent.data
	}
	dc, err := s.getWork(ent, 10*time.Second)
	if err != nil {
		return nil
	}
	ent.data = dc
	go s.readUDPPackets(ent, dc)
	return dc
}

func (s *Server) dropUDPData(ent *proxyEnt) {
	ent.wmu.Lock()
	if ent.data != nil {
		_ = ent.data.Close()
		ent.data = nil
	}
	ent.wmu.Unlock()
}

// readUDPPackets parses reply packets from the client and forwards them to
// the recorded visitor address.
func (s *Server) readUDPPackets(ent *proxyEnt, dc net.Conn) {
	for {
		hdr, payload, err := proto.ReadPacket(dc)
		if err != nil || ent.dead.Load() {
			s.dropUDPData(ent)
			return
		}
		raddr, err := net.ResolveUDPAddr("udp", str(hdr, "raddr"))
		if err != nil {
			continue
		}
		_, _ = ent.udpLn.WriteToUDP(payload, raddr)
		ent.cnt.In.Add(uint64(len(payload)))
	}
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}
