// Package client implements the lumoc (tunnel client): a resilient control
// connection to the server, proxy registration and work-connection handling.
package client

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/j353719624/lumo/pkg/config"
	"github.com/j353719624/lumo/pkg/log"
	"github.com/j353719624/lumo/pkg/proto"
	"github.com/j353719624/lumo/pkg/relay"
	"github.com/j353719624/lumo/pkg/udpstream"
	"github.com/j353719624/lumo/pkg/version"
)

// Client is a running lumoc instance.
type Client struct {
	cfg     *config.ClientConfig
	cfgPath string
	id      string

	// Log, when set, captures this connection's events in a scoped ring.
	Log *log.Instance

	mu      sync.Mutex
	proxies map[string]*config.ProxyConfig
	state   map[string]*ProxyState
	stats   map[string]*relay.Counters
	ctrl    net.Conn
	lastErr string

	muxPort int
	// usess is the multiplexed KCP session when Protocol is "kcp".
	usess *udpstream.Session

	punchMu      sync.Mutex
	punchSvc     map[string]*punchService
	punchSess    map[string]*udpstream.Session
	punchWaiters map[string]chan proto.Msg

	pendMu  sync.Mutex
	pending map[string]chan proto.Msg

	wmu    sync.Mutex
	closed atomic.Bool
	admin  *httpServer
}

// ProxyState is the dashboard-visible state of one proxy.
type ProxyState struct {
	Status string
	Err    string
	Since  time.Time
}

// New creates a Client from validated configuration.
func New(cfg *config.ClientConfig, cfgPath string) *Client {
	c := &Client{
		cfg:         cfg,
		cfgPath:     cfgPath,
		id:          proto.RandomHex(8),
		proxies:     map[string]*config.ProxyConfig{},
		state:       map[string]*ProxyState{},
		stats:       map[string]*relay.Counters{},
		pending:     map[string]chan proto.Msg{},
		punchSvc:     map[string]*punchService{},
		punchSess:    map[string]*udpstream.Session{},
		punchWaiters: map[string]chan proto.Msg{},
	}
	for i := range cfg.Proxies {
		p := cfg.Proxies[i]
		c.proxies[p.Name] = &c.cfg.Proxies[i]
		c.state[p.Name] = &ProxyState{Status: "waiting", Since: time.Now()}
		c.stats[p.Name] = &relay.Counters{}
	}
	for i := range cfg.Visitors {
		v := cfg.Visitors[i]
		c.stats[v.Name] = &relay.Counters{}
	}
	return c
}

func (c *Client) addr() string {
	return net.JoinHostPort(c.cfg.ServerAddr, fmt.Sprintf("%d", c.cfg.ServerPort))
}

// dataConn opens a data connection to the server: a multiplexed KCP stream
// when the KCP transport is active, otherwise a plain TCP connection.
func (c *Client) dataConn() (net.Conn, error) {
	c.mu.Lock()
	us := c.usess
	c.mu.Unlock()
	if us != nil {
		return us.Open()
	}
	return net.DialTimeout("tcp", c.addr(), 10*time.Second)
}

// logi/logw/loge/logd route messages to the scoped instance when present.
func (c *Client) logd(f string, a ...any) {
	if c.Log != nil {
		c.Log.Debugf(f, a...)
	} else {
		log.Debugf(f, a...)
	}
}
func (c *Client) logi(f string, a ...any) {
	if c.Log != nil {
		c.Log.Infof(f, a...)
	} else {
		log.Infof(f, a...)
	}
}
func (c *Client) logw(f string, a ...any) {
	if c.Log != nil {
		c.Log.Warnf(f, a...)
	} else {
		log.Warnf(f, a...)
	}
}
func (c *Client) loge(f string, a ...any) {
	if c.Log != nil {
		c.Log.Errorf(f, a...)
	} else {
		log.Errorf(f, a...)
	}
}

// Run blocks until ctx is cancelled, reconnecting with backoff.
func (c *Client) Run(ctx context.Context) error {
	if c.cfg.Web.Port > 0 {
		go c.runAdmin(ctx)
	}
	if len(c.cfg.Visitors) > 0 {
		go c.runVisitors(ctx)
	}
	c.logi("lumoc %s starting (server %s)", version.Version, c.addr())
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.serve(ctx)
		if ctx.Err() != nil {
			return nil
		}
		c.mu.Lock()
		c.lastErr = err.Error()
		c.mu.Unlock()
		c.logw("connection lost: %v; retrying in %s", err, backoff)
		c.markAllOffline()
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 15*time.Second {
			backoff = 15 * time.Second
		}
	}
}

func (c *Client) markAllOffline() {
	c.mu.Lock()
	for _, st := range c.state {
		st.Status = "offline"
	}
	c.ctrl = nil
	c.mu.Unlock()
}

// serve runs one connection lifetime and returns when it breaks.
func (c *Client) serve(ctx context.Context) error {
	var conn net.Conn
	if c.cfg.Protocol == "kcp" {
		// KCP：一条 UDP 会话多路复用，控制流与数据流共用
		kaddr := net.JoinHostPort(c.cfg.ServerAddr, strconv.Itoa(c.cfg.KCPPort))
		kudp, err := net.ResolveUDPAddr("udp", kaddr)
		if err != nil {
			return fmt.Errorf("kcp addr %s: %w", kaddr, err)
		}
		us, err := udpstream.Dial(kudp, 10*time.Second)
		if err != nil {
			return fmt.Errorf("kcp dial %s: %w", kaddr, err)
		}
		defer us.Close()
		c.mu.Lock()
		c.usess = us
		c.mu.Unlock()
		ctrl, err := us.Open()
		if err != nil {
			return fmt.Errorf("kcp control stream: %w", err)
		}
		conn = ctrl
		c.logi("KCP transport enabled (udp %s)", kaddr)
	} else {
		rawConn, err := net.DialTimeout("tcp", c.addr(), 10*time.Second)
		if err != nil {
			return fmt.Errorf("dial %s: %w", c.addr(), err)
		}
		defer rawConn.Close()

		// TLS 传输加密
		if c.cfg.TLSEnable {
			tlsConn := tls.Client(rawConn, &tls.Config{InsecureSkipVerify: true})
			if err := tlsConn.Handshake(); err != nil {
				return fmt.Errorf("tls handshake: %w", err)
			}
			conn = tlsConn
			c.logi("TLS transport enabled")
		} else {
			conn = rawConn
		}
	}
	defer conn.Close()
	c.mu.Lock()
	c.ctrl = conn
	c.mu.Unlock()

	nonce, _ := hex.DecodeString(proto.RandomHex(16))
	hello := proto.Msg{
		"type":   "hello",
		"ver":    proto.Version,
		"client": c.id,
		"nonce":  hex.EncodeToString(nonce),
		"auth":   proto.Sign(c.cfg.AuthToken, nonce),
	}
	if c.cfg.Name != "" {
		hello["name"] = c.cfg.Name
	}
	pv := c.cfg.ProtocolVersion
	if pv <= 0 {
		pv = 2
	}
	hello["pv"] = pv
	if err := proto.Send(conn, hello); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	welcome, err := proto.Recv(conn)
	if err != nil {
		return fmt.Errorf("read welcome: %w", err)
	}
	if welcome.Str("type") != "welcome" {
		return fmt.Errorf("server rejected login: %s", welcome.Str("message"))
	}
	c.mu.Lock()
	c.muxPort = welcome.Int("mux_port")
	if kp := welcome.Int("kcp_port"); kp > 0 && kp != c.cfg.KCPPort {
		c.cfg.KCPPort = kp // 服务端改了 KCP 端口：下次重连直接用新端口
		c.logi("server kcp port updated to %d", kp)
	}
	c.mu.Unlock()
	c.mu.Lock()
	c.lastErr = ""
	c.mu.Unlock()
	c.logi("connected to %s", c.addr())

	// The read loop must run before registration: reg replies are only
	// delivered through it, and registerAll blocks on those replies.
	readErr := make(chan error, 1)
	serveCtx, serveCancel := context.WithCancel(ctx)
	defer serveCancel()
	go func() { readErr <- c.readLoop(serveCtx, conn) }()

	if err := c.registerAll(); err != nil {
		return err
	}
	// 注册被拒的代理（典型：KCP 旧会话尚未超时，名字还被占着）持续重试
	go c.retryFailedRegistrations(serveCtx, conn)
	// PUNCH 服务侧：维持打洞 socket 并上报公网地址
	c.mu.Lock()
	for name, p := range c.proxies {
		if p.Type == "punch" {
			go c.runPunchService(serveCtx, name)
		}
	}
	c.mu.Unlock()

	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()
	for {
		select {
		case err := <-readErr:
			return err
		case <-pingTicker.C:
			if !c.send(proto.Msg{"type": "ping"}) {
				return fmt.Errorf("control write failed")
			}
		case <-ctx.Done():
			_ = conn.Close()
			return nil
		}
	}
}

// readLoop consumes control messages until the connection breaks. Replies
// to reg/unreg are routed to the waiting request; work requests spawn a
// fresh data connection.
func (c *Client) readLoop(ctx context.Context, conn net.Conn) error {
	for {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		m, err := proto.Recv(conn)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("control read: %w", err)
		}
		switch m.Str("type") {
		case "work":
			go c.handleWork(m)
		case "punch_peer", "punch_fail":
			c.handlePunchMessage(m)
		case "reg_ok", "reg_err", "unreg_ok", "unreg_err":
			c.deliver(m)
		case "pong":
		default:
			c.logd("ignoring message type %q", m.Str("type"))
		}
	}
}

// ProxyStatus is one proxy's dashboard-visible snapshot.
type ProxyStatus struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Err    string `json:"err,omitempty"`
	In     uint64 `json:"in"`
	Out    uint64 `json:"out"`
}

// State reports engine health for external dashboards: whether the control
// connection is up, the last failure (empty when healthy) and per-proxy
// status with traffic counters.
func (c *Client) State() (running bool, lastErr string, proxies []ProxyStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	running = c.ctrl != nil
	lastErr = c.lastErr
	proxies = make([]ProxyStatus, 0, len(c.proxies))
	for name, p := range c.proxies {
		ps := ProxyStatus{Name: name, Type: p.Type}
		if st := c.state[name]; st != nil {
			ps.Status, ps.Err = st.Status, st.Err
		}
		if cnt := c.stats[name]; cnt != nil {
			ps.In, ps.Out = cnt.In.Load(), cnt.Out.Load()
		}
		proxies = append(proxies, ps)
	}
	return running, lastErr, proxies
}

// send writes one control message; returns false when the connection is gone.
func (c *Client) send(m proto.Msg) bool {
	c.mu.Lock()
	conn := c.ctrl
	c.mu.Unlock()
	if conn == nil {
		return false
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return proto.Send(conn, m) == nil
}

func (c *Client) deliver(m proto.Msg) {
	c.pendMu.Lock()
	ch := c.pending[m.Str("name")]
	c.pendMu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (c *Client) request(m proto.Msg, timeout time.Duration) proto.Msg {
	name := m.Str("name")
	ch := make(chan proto.Msg, 1)
	c.pendMu.Lock()
	c.pending[name] = ch
	c.pendMu.Unlock()
	defer func() {
		c.pendMu.Lock()
		delete(c.pending, name)
		c.pendMu.Unlock()
	}()
	if !c.send(m) {
		return proto.Msg{"type": "reg_err", "message": "not connected"}
	}
	select {
	case reply := <-ch:
		return reply
	case <-time.After(timeout):
		return proto.Msg{"type": "reg_err", "message": "timed out"}
	}
}

// registerAll sends a reg for every configured proxy and records results.
func (c *Client) registerAll() error {
	c.mu.Lock()
	proxies := make([]*config.ProxyConfig, 0, len(c.proxies))
	for _, p := range c.proxies {
		proxies = append(proxies, p)
	}
	c.mu.Unlock()
	for _, p := range proxies {
		reply := c.request(proto.Msg{
			"type":        "reg",
			"name":        p.Name,
			"label":       p.Label,
			"kind":        p.Type,
			"remote_port": p.RemotePort,
			"sk":          p.SK,
			"domains":     p.CustomDomains,
			"sub_paths":   p.SubPaths,
			"http_user":   p.HTTPUser,
			"http_pass":   p.HTTPPass,
		}, 10*time.Second)
		c.mu.Lock()
		if reply.Str("type") == "reg_ok" {
			c.state[p.Name] = &ProxyState{Status: "running", Since: time.Now()}
			c.logi("proxy %q (%s) registered", p.Name, p.Type)
		} else {
			c.state[p.Name] = &ProxyState{Status: "error", Err: reply.Str("message"), Since: time.Now()}
			c.loge("proxy %q (%s) rejected: %s", p.Name, p.Type, reply.Str("message"))
		}
		c.mu.Unlock()
	}
	return nil
}

// retryFailedRegistrations re-registers proxies that were rejected, so a
// restart that races the previous session's timeout heals by itself.
func (c *Client) retryFailedRegistrations(ctx context.Context, conn net.Conn) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		var pending []*config.ProxyConfig
		for name, st := range c.state {
			if st.Status == "error" {
				if p, ok := c.proxies[name]; ok {
					pending = append(pending, p)
				}
			}
		}
		c.mu.Unlock()
		for _, p := range pending {
			reply := c.request(proto.Msg{
				"type":        "reg",
				"name":        p.Name,
				"label":       p.Label,
				"kind":        p.Type,
				"remote_port": p.RemotePort,
				"sk":          p.SK,
				"domains":     p.CustomDomains,
				"sub_paths":   p.SubPaths,
				"http_user":   p.HTTPUser,
				"http_pass":   p.HTTPPass,
			}, 10*time.Second)
			if reply.Str("type") == "reg_ok" {
				c.mu.Lock()
				c.state[p.Name] = &ProxyState{Status: "running", Since: time.Now()}
				c.mu.Unlock()
				c.logi("proxy %q (%s) registered after retry", p.Name, p.Type)
			}
		}
	}
}

// unregisterAll removes every proxy from the server.
func (c *Client) unregisterAll() {
	c.mu.Lock()
	proxies := make([]*config.ProxyConfig, 0, len(c.proxies))
	for _, p := range c.proxies {
		proxies = append(proxies, p)
	}
	c.mu.Unlock()
	for _, p := range proxies {
		_ = c.request(proto.Msg{"type": "unreg", "name": p.Name}, 5*time.Second)
	}
}

// Reload re-reads the config file and applies the proxy set without
// dropping the control connection.
func (c *Client) Reload() error {
	cfg, err := config.LoadClient(c.cfgPath)
	if err != nil {
		return err
	}
	c.unregisterAll()
	c.mu.Lock()
	c.cfg = cfg
	c.proxies = map[string]*config.ProxyConfig{}
	c.state = map[string]*ProxyState{}
	c.stats = map[string]*relay.Counters{}
	for i := range cfg.Proxies {
		p := &cfg.Proxies[i]
		c.proxies[p.Name] = p
		c.state[p.Name] = &ProxyState{Status: "waiting", Since: time.Now()}
		c.stats[p.Name] = &relay.Counters{}
	}
	c.mu.Unlock()
	return c.registerAll()
}

// verifyAuth performs a dry-run handshake, used by `lumoc verify`.
func (c *Client) verifyAuth() error {
	conn, err := net.DialTimeout("tcp", c.addr(), 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	nonce, _ := hex.DecodeString(proto.RandomHex(16))
	if err := proto.Send(conn, proto.Msg{
		"type": "hello", "ver": proto.Version, "client": "verify",
		"nonce": hex.EncodeToString(nonce), "auth": proto.Sign(c.cfg.AuthToken, nonce),
	}); err != nil {
		return err
	}
	m, err := proto.Recv(conn)
	if err != nil {
		return err
	}
	if m.Str("type") != "welcome" {
		return fmt.Errorf("%s", m.Str("message"))
	}
	return nil
}
