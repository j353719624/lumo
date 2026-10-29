package client

// PUNCH: peer-to-peer tunnels. Both sides learn their public UDP address via
// STUN, exchange it through the server, punch a hole and run a multiplexed
// udpstream session directly between the two clients. When punching fails
// the visitor silently falls back to the server relay (sealed-tcp-style).

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"time"

	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/log"
	"github.com/j353719624/lumo/pkg/proto"
	"github.com/j353719624/lumo/pkg/relay"
	"github.com/j353719624/lumo/pkg/stun"
	"github.com/j353719624/lumo/pkg/udpstream"
)

type punchService struct {
	pc *net.UDPConn
}

// stunServers returns the configured server first, then public fallbacks.
func (c *Client) stunServers() []string {
	list := []string{}
	if c.cfg.StunServer != "" {
		list = append(list, c.cfg.StunServer)
	}
	for _, s := range []string{"stun.easyvoip.com:3478", "stun.l.google.com:19302", "stun.cloudflare.com:3478"} {
		dup := false
		for _, have := range list {
			if have == s {
				dup = true
				break
			}
		}
		if !dup {
			list = append(list, s)
		}
	}
	return list
}

// discover tries the STUN servers in order until one answers.
func (c *Client) discover(pc *net.UDPConn) (*net.UDPAddr, error) {
	var lastErr error
	for _, srv := range c.stunServers() {
		addr, err := stun.Discover(pc, srv, 3*time.Second)
		if err == nil {
			return addr, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// runPunchService keeps the punch socket of a service-side punch proxy open,
// reporting its reflexive address to the server.
func (c *Client) runPunchService(ctx context.Context, name string) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		c.loge("punch %q: punch socket: %v", name, err)
		return
	}
	c.punchMu.Lock()
	c.punchSvc[name] = &punchService{pc: pc}
	c.punchMu.Unlock()
	defer func() {
		c.punchMu.Lock()
		delete(c.punchSvc, name)
		c.punchMu.Unlock()
		_ = pc.Close()
	}()

	report := func() {
		addr, err := c.discover(pc)
		if err != nil {
			c.logw("punch %q: STUN discovery failed: %v", name, err)
			return
		}
		c.send(proto.Msg{"type": "punch_addr", "name": name, "addr": addr.String()})
		c.logi("punch %q: punch address %s", name, addr)
	}
	report()
	t := time.NewTicker(25 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			report()
		}
	}
}

// handlePunchPeer (service side) adopts a punched session and serves streams.
func (c *Client) handlePunchPeer(m proto.Msg) {
	name := m.Str("name")
	c.punchMu.Lock()
	svc := c.punchSvc[name]
	c.punchMu.Unlock()
	if svc == nil {
		return
	}
	peer, err := net.ResolveUDPAddr("udp", m.Str("addr"))
	if err != nil {
		return
	}
	sess := udpstream.Adopt(svc.pc, peer, uint32(m.Int("conv")))
	c.logi("punch %q: peer session to %s", name, peer)
	// 主动探测：为 NAT 打洞打开本方 pinhole
	go sess.Punch(15, 150*time.Millisecond)
	go func() {
		for {
			st, err := sess.Accept()
			if err != nil {
				c.logd("punch %q: peer session closed: %v", name, err)
				return
			}
			go c.servePunchStream(name, st)
		}
	}()
}

func (c *Client) servePunchStream(name string, st net.Conn) {
	c.mu.Lock()
	p := c.proxies[name]
	c.mu.Unlock()
	if p == nil {
		_ = st.Close()
		return
	}
	local := net.JoinHostPort(p.LocalIP, strconv.Itoa(p.LocalPort))
	lc, err := net.DialTimeout("tcp", local, 5*time.Second)
	if err != nil {
		c.loge("punch %q: dial local %s: %v", name, local, err)
		_ = st.Close()
		return
	}
	bw, _ := config.ParseBandwidth(p.Bandwidth)
	relay.LimitedPipe(st, lc, c.counters(name), bw)
}

// visitorPunch serves a local listener, preferring the P2P path.
func (c *Client) visitorPunch(ctx context.Context, v config.VisitorConfig) error {
	addr := net.JoinHostPort(v.BindAddr, strconv.Itoa(v.BindPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	c.logi("visitor %q listening on %s -> punch %q", v.Name, addr, v.ServerName)
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
			cnt := c.stats[v.Name]
			if st, err := c.punchDial(ctx, v); err == nil {
				relay.Pipe(lc, st, cnt)
				return
			} else {
				c.logw("visitor %q: P2P unavailable (%v), using relay", v.Name, err)
			}
			// 回退：经服务器中继（与 sealed-tcp 相同的数据通道）
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
			relay.Pipe(lc, dc, cnt)
		}(lc)
	}
}

// punchDial establishes (or reuses) a punched session and opens a stream.
func (c *Client) punchDial(ctx context.Context, v config.VisitorConfig) (net.Conn, error) {
	c.punchMu.Lock()
	if s := c.punchSess[v.Name]; s != nil && !s.Dead() {
		c.punchMu.Unlock()
		return s.Open()
	}
	c.punchMu.Unlock()

	pc, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	mapped, err := c.discover(pc)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	ch := make(chan proto.Msg, 1)
	c.punchMu.Lock()
	c.punchWaiters[v.ServerName] = ch
	c.punchMu.Unlock()
	defer func() {
		c.punchMu.Lock()
		delete(c.punchWaiters, v.ServerName)
		c.punchMu.Unlock()
	}()

	if !c.send(proto.Msg{"type": "punch_punch", "name": v.ServerName, "sk": v.SK, "addr": mapped.String()}) {
		_ = pc.Close()
		return nil, errors.New("control write failed")
	}
	var reply proto.Msg
	select {
	case reply = <-ch:
	case <-time.After(6 * time.Second):
		_ = pc.Close()
		return nil, errors.New("punch coordination timeout")
	case <-ctx.Done():
		_ = pc.Close()
		return nil, ctx.Err()
	}
	if reply.Str("type") == "punch_fail" {
		_ = pc.Close()
		return nil, fmt.Errorf("peer unavailable: %s", reply.Str("error"))
	}
	peer, err := net.ResolveUDPAddr("udp", reply.Str("addr"))
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	log.Infof("visitor %q: mapped %s, peer %s, conv %d", v.Name, mapped, peer, reply.Int("conv"))
	sess, err := udpstream.DialOn(pc, peer, uint32(reply.Int("conv")), 5*time.Second)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	c.punchMu.Lock()
	c.punchSess[v.Name] = sess
	c.punchMu.Unlock()
	log.Infof("visitor %q: P2P session established with %s", v.Name, peer)
	return sess.Open()
}

// handlePunchMessage routes peer/fail replies either to the visitor waiter or
// to the service-side punch handler.
func (c *Client) handlePunchMessage(m proto.Msg) {
	name := m.Str("name")
	c.punchMu.Lock()
	ch := c.punchWaiters[name]
	svc := c.punchSvc[name]
	c.punchMu.Unlock()
	if ch != nil {
		select {
		case ch <- m:
		default:
		}
		return
	}
	if svc != nil && m.Str("type") == "punch_peer" {
		go c.handlePunchPeer(m)
	}
}
