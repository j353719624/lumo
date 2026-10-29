// Package udpstream implements LUMO's KCP-style transport: a reliable,
// ordered, multiplexed byte-stream layer over UDP using only the standard
// library.
//
// One Session spans one UDP 5-tuple and carries several streams. Reliability
// per stream comes from sequence numbers, cumulative + selective ACKs, a
// sliding send window and RTO-based retransmission. Each stream is exposed as
// a net.Conn, so the tunnel's control protocol and data relays run over it
// unchanged.
//
// Datagram layout (big-endian):
//
//	conv u32 | cmd u8 | stream u8 | flags u8 | seq u32 | ack u32 |
//	sackN u8 | sack u32×sackN | payload
package udpstream

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxPayload    = 1200             // datagram payload cap, fits typical MTU
	window        = 96               // packets in flight per stream
	ackSackCap    = 24               // selective-ACK entries per datagram
	pingInterval  = 6 * time.Second  // NAT keepalive
	deadTimeout   = 25 * time.Second // session silence before teardown (pings every 6s)
	minRTO        = 60 * time.Millisecond
	maxRTO        = time.Second
	initialRTO    = 250 * time.Millisecond
	synRetryEvery = 400 * time.Millisecond
	synTimeout    = 6 * time.Second
	pumpTick      = 20 * time.Millisecond
	maxStreams    = 250
)

const (
	cmdSYN uint8 = iota
	cmdSYNACK
	cmdPSH
	cmdACK
	cmdPING
)

const flagFIN uint8 = 0x80

var (
	errSessionDead = errors.New("udpstream: session dead")
	errStreamReset = errors.New("udpstream: stream reset")
)

// ---------------------------------------------------------------------------
// wire format

type wirePkt struct {
	conv    uint32
	cmd     uint8
	stream  uint8
	fin     bool
	seq     uint32
	ack     uint32
	sack    []uint32
	payload []byte
}

func encodePkt(p wirePkt) []byte {
	out := make([]byte, 16, 16+len(p.sack)*4+len(p.payload))
	binary.BigEndian.PutUint32(out[0:4], p.conv)
	out[4] = p.cmd
	out[5] = p.stream
	out[6] = 0
	if p.fin {
		out[6] |= 0x80
	}
	binary.BigEndian.PutUint32(out[7:11], p.seq)
	binary.BigEndian.PutUint32(out[11:15], p.ack)
	out[15] = uint8(len(p.sack))
	for _, a := range p.sack {
		var q [4]byte
		binary.BigEndian.PutUint32(q[:], a)
		out = append(out, q[:]...)
	}
	return append(out, p.payload...)
}

func decodePkt(b []byte) (wirePkt, error) {
	if len(b) < 15 {
		return wirePkt{}, errors.New("udpstream: short datagram")
	}
	p := wirePkt{
		conv:   binary.BigEndian.Uint32(b[0:4]),
		cmd:    b[4],
		stream: b[5],
		fin:    b[6]&0x80 != 0,
		seq:    binary.BigEndian.Uint32(b[7:11]),
		ack:    binary.BigEndian.Uint32(b[11:15]),
	}
	sackN := int(b[15])
	if need := 16 + sackN*4; len(b) < need {
		return wirePkt{}, errors.New("udpstream: short sack")
	}
	for i := 0; i < sackN; i++ {
		p.sack = append(p.sack, binary.BigEndian.Uint32(b[16+i*4:20+i*4]))
	}
	// 复制负载：调用方的读取缓冲会被复用，不能让 payload 别名指向它
	p.payload = append([]byte(nil), b[16+sackN*4:]...)
	return p, nil
}

func randomUint32() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

// ---------------------------------------------------------------------------
// stream: one multiplexed, reliable, ordered channel

type segment struct {
	data     []byte
	sent     time.Time
	tries    int
	lastRTO  time.Duration
	transmit bool // 已重传过（RTT 采样只统计首发）
}

type stream struct {
	id   uint8
	sess *Session

	mu           sync.Mutex
	rmu          sync.Mutex        // 读截止时间锁
	rxWant       uint32            // 下一个期望到达的 seq
	nextSeq      uint32            // 发送侧下一个 seq
	pendErr      error             // 关闭/死亡时读取返回的错误
	accepted     *atomic.Bool      // 服务端：是否已被 Accept 取走
	reorder      map[uint32][]byte // 乱序暂存
	inflight     map[uint32]*segment
	finSeq       uint32 // FIN 所在 seq
	finSeen      bool
	fin          bool // 流已收尾（数据读完）
	finErr       error
	pend         []byte
	rto          time.Duration
	rtt          time.Duration
	notify       chan struct{}
	closed       atomic.Bool
	acc          *atomic.Bool
	writeDead    bool
	readDeadline time.Time
}

func (st *stream) signal() {
	select {
	case st.notify <- struct{}{}:
	default:
	}
}

func (st *stream) seqBefore(a, b uint32) bool { return int32(a-b) < 0 }

func (st *stream) setReadDeadline(t time.Time) {
	st.rmu.Lock()
	st.readDeadline = t
	st.rmu.Unlock()
	st.signal()
}

func (st *stream) read(p []byte) (int, error) {
	for {
		st.mu.Lock()
		if len(st.pend) > 0 {
			n := copy(p, st.pend)
			st.pend = st.pend[n:]
			drained := len(st.pend) == 0
			var err error
			if drained && st.fin {
				err = io.EOF
			}
			st.mu.Unlock()
			if drained && st.fin && err != nil {
				return n, err
			}
			if n > 0 {
				return n, nil
			}
			continue
		}
		if st.fin {
			err := io.EOF
			st.mu.Unlock()
			return 0, err
		}
		if st.closed.Load() {
			err := errStreamReset
			st.mu.Unlock()
			return 0, err
		}
		deadline := st.readDeadline
		st.mu.Unlock()

		var timeout <-chan time.Time
		if !deadline.IsZero() {
			timer := time.NewTimer(time.Until(deadline))
			timeout = timer.C
			defer func(t *time.Timer) { t.Stop() }(timer)
		}
		select {
		case <-st.notify:
		case <-timeout:
			return 0, os.ErrDeadlineExceeded
		}
	}
}

func (st *stream) write(p []byte) (int, error) {
	sent := 0
	for len(p) > 0 {
		st.mu.Lock()
		full := len(st.inflight) >= window
		dead := st.writeDead || st.sess.dead.Load() || st.closed.Load()
		st.mu.Unlock()
		if dead {
			return sent, errStreamReset
		}
		if full {
			time.Sleep(4 * time.Millisecond)
			continue
		}
		n := len(p)
		if n > maxPayload {
			n = maxPayload
		}
		if err := st.sendChunk(p[:n], false); err != nil {
			return sent, err
		}
		p = p[n:]
		sent += n
	}
	return sent, nil
}

func (st *stream) sendChunk(chunk []byte, fin bool) error {
	st.mu.Lock()
	if st.writeDead && !fin {
		st.mu.Unlock()
		return errStreamReset
	}
	seq := st.nextSeq
	st.nextSeq++
	seg := &segment{data: append([]byte(nil), chunk...), sent: time.Now(), tries: 1, lastRTO: initialRTO}
	st.inflight[seq] = seg
	if fin {
		st.finSeq = seq
		st.finSeen = false // FIN 待对端确认语义上随数据生效
	}
	rxWant := st.rxWant
	st.mu.Unlock()
	return st.sess.send(wirePkt{
		conv:    st.sess.conv,
		cmd:     cmdPSH,
		stream:  st.id,
		fin:     fin,
		seq:     seq,
		ack:     rxWant,
		payload: seg.data,
	})
}

// closeWrite 发送 FIN，结束本地写侧（流正常收尾）。
func (st *stream) closeWrite() {
	st.mu.Lock()
	already := st.writeDead
	st.writeDead = true
	st.mu.Unlock()
	if !already {
		_ = st.sendChunk(nil, true)
	}
}

// close 硬重置流。
func (st *stream) close(err error) {
	st.closed.Store(true)
	st.mu.Lock()
	st.pendErr = err
	st.mu.Unlock()
	st.signal()
}

// Conn exposes one stream as a net.Conn.
type Conn struct {
	s *stream
}

func (c *Conn) Read(p []byte) (int, error)  { return c.s.read(p) }
func (c *Conn) Write(p []byte) (int, error) { return c.s.write(p) }
func (c *Conn) Close() error                { c.s.closeWrite(); return nil }
func (c *Conn) LocalAddr() net.Addr         { return c.s.sess.remote }
func (c *Conn) RemoteAddr() net.Addr        { return c.s.sess.remote }
func (c *Conn) SetDeadline(t time.Time) error {
	return c.SetReadDeadline(t)
}
func (c *Conn) SetReadDeadline(t time.Time) error  { c.s.setReadDeadline(t); return nil }
func (c *Conn) SetWriteDeadline(t time.Time) error { return nil }

// ---------------------------------------------------------------------------
// Session

// Session is one reliable multiplexed connection over a UDP 5-tuple.
type Session struct {
	pc      *net.UDPConn
	remote  *net.UDPAddr
	conv    uint32
	isServ  bool
	onNewFn func(s *Session, id uint8)

	mu      sync.Mutex
	streams map[uint8]*stream
	dead    atomic.Bool
	closed  atomic.Bool

	lastRx atomic.Int64
	pkts   chan wirePkt
	wg     sync.WaitGroup
}

func (s *Session) newStreamLocked(id uint8) *stream {
	st := &stream{
		id:       id,
		sess:     s,
		reorder:  map[uint32][]byte{},
		inflight: map[uint32]*segment{},
		notify:   make(chan struct{}, 1),
		rto:      initialRTO,
		acc:      new(atomic.Bool),
	}
	s.streams[id] = st
	return st
}

// Open creates a new stream (client side). The caller dials with it.
func (s *Session) Open() (net.Conn, error) {
	if s.dead.Load() || s.closed.Load() {
		return nil, errSessionDead
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := uint8(1); id < 255; id++ {
		if _, used := s.streams[id]; !used {
			st := s.newStreamLocked(id)
			st.acc.Store(true) // 自己打开的流无需 Accept
			go st.sendChunk(nil, false)
			return &Conn{s: st}, nil
		}
	}
	return nil, errors.New("udpstream: no free stream ids")
}

// Accept waits for the peer to open a new stream (server side).
func (s *Session) Accept() (net.Conn, error) {
	for {
		if s.dead.Load() || s.closed.Load() {
			return nil, errSessionDead
		}
		s.mu.Lock()
		for id, st := range s.streams {
			if id == 0 {
				continue
			}
			if st.acc.CompareAndSwap(false, true) {
				s.mu.Unlock()
				return &Conn{s: st}, nil
			}
		}
		s.mu.Unlock()
		time.Sleep(40 * time.Millisecond)
	}
}

// Control returns the session's stream 0 (control channel).
func (s *Session) Control() net.Conn {
	s.mu.Lock()
	st := s.streams[0]
	s.mu.Unlock()
	if st == nil {
		return nil
	}
	return &Conn{s: st}
}

// Close tears the session down.
func (s *Session) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	s.dead.Store(true)
	s.closeAll(errSessionDead)
	return nil
}

func (s *Session) closeAll(err error) {
	s.mu.Lock()
	streams := make([]*stream, 0, len(s.streams))
	for _, st := range s.streams {
		streams = append(streams, st)
	}
	s.mu.Unlock()
	for _, st := range streams {
		st.close(err)
	}
}

func (s *Session) send(p wirePkt) error {
	buf := encodePkt(p)
	_, err := s.pc.WriteToUDP(buf, s.remote)
	return err
}

func (s *Session) rxSeqOf(id uint8) uint32 {
	s.mu.Lock()
	st := s.streams[id]
	s.mu.Unlock()
	if st == nil {
		return 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.rxWant
}

// pump 处理会话的收包、重传、保活与死亡检测。
func (s *Session) pump() {
	defer s.wg.Done()
	t := time.NewTicker(pumpTick)
	defer t.Stop()
	lastPing := time.Now()
	for {
		if s.dead.Load() || s.closed.Load() {
			s.closeAll(errSessionDead)
			return
		}
		select {
		case p := <-s.pkts:
			s.lastRx.Store(time.Now().UnixNano())
			switch p.cmd {
			case cmdPSH:
				s.rxPSH(p)
			case cmdACK:
				s.rxACK(p)
			case cmdPING:
				// 仅保活
			}
		case <-t.C:
			s.retransmit()
			if time.Since(lastPing) >= pingInterval {
				lastPing = time.Now()
				_ = s.send(wirePkt{conv: s.conv, cmd: cmdPING})
			}
			if time.Since(time.Unix(0, s.lastRx.Load())) > deadTimeout {
				s.dead.Store(true)
				s.closeAll(errSessionDead)
				return
			}
		}
	}
}

func (s *Session) streamOf(id uint8) *stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[id]
}

// rxPSH 处理一个到达的数据分片：按序交付 + 立即 ACK。
func (s *Session) rxPSH(p wirePkt) {
	st := s.streamOf(p.stream)
	if st == nil {
		if !s.isServ || p.stream == 0 {
			return
		}
		s.mu.Lock()
		if int(p.stream) >= maxStreams {
			s.mu.Unlock()
			return
		}
		if _, exists := s.streams[p.stream]; exists {
			s.mu.Unlock()
			return
		}
		st = s.newStreamLocked(p.stream)
		s.mu.Unlock()
		if s.onNewFn != nil {
			s.onNewFn(s, p.stream)
		}
	}

	fin := p.fin
	var finErr error
	st.mu.Lock()
	if p.seq == st.rxWant {
		st.pend = append(st.pend, p.payload...)
		st.rxWant++
		for {
			chunk, ok := st.reorder[st.rxWant]
			if !ok {
				break
			}
			delete(st.reorder, st.rxWant)
			st.pend = append(st.pend, chunk...)
			st.rxWant++
		}
		if fin {
			st.fin = true
			finErr = io.EOF
		}
	} else if st.seqBefore(p.seq, st.rxWant) {
		st.mu.Unlock()
		_ = s.sendACK(p.stream, st.rxWant, st.sackSnapshot())
		return
	} else {
		st.reorder[p.seq] = append([]byte(nil), p.payload...)
		if fin {
			st.finSeq = p.seq
			st.finSeen = true
		}
		rxWant := st.rxWant
		sacks := st.sackSnapshot()
		st.mu.Unlock()
		_ = s.sendACK(p.stream, rxWant, sacks)
		return
	}
	rxWant := st.rxWant
	sacks := st.sackSnapshot()
	if fin {
		st.finErr = finErr
	}
	st.mu.Unlock()

	st.signal()
	_ = s.sendACK(p.stream, rxWant, sacks)
	_ = finErr
}

func (s *Session) sendACK(stream uint8, ack uint32, sacks []uint32) error {
	return s.send(wirePkt{conv: s.conv, cmd: cmdACK, stream: stream, ack: ack, sack: sacks})
}

func (st *stream) sackSnapshot() []uint32 {
	ids := make([]uint32, 0, len(st.reorder))
	for seq := range st.reorder {
		ids = append(ids, seq)
	}
	// 排序后最多带 ackSackCap 个
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if int32(ids[j]-ids[i]) < 0 {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	if len(ids) > ackSackCap {
		ids = ids[:ackSackCap]
	}
	return ids
}

// rxACK 处理对端确认：移除已确认分片、采样 RTT、唤醒写侧。
func (s *Session) rxACK(p wirePkt) {
	st := s.streamOf(p.stream)
	if st == nil {
		return
	}
	now := time.Now()
	st.mu.Lock()
	for seq, seg := range st.inflight {
		if st.seqBefore(seq, p.ack) {
			if !seg.transmit {
				sample := now.Sub(seg.sent)
				if st.rtt == 0 {
					st.rtt = sample
				} else {
					st.rtt = (7*st.rtt + sample) / 8
				}
				st.rto = st.rtt * 2
				if st.rto < minRTO {
					st.rto = minRTO
				}
				if st.rto > maxRTO {
					st.rto = maxRTO
				}
			}
			delete(st.inflight, seq)
		}
	}
	for _, sa := range p.sack {
		if seg, ok := st.inflight[sa]; ok {
			if !seg.transmit {
				sample := now.Sub(seg.sent)
				if st.rtt == 0 {
					st.rtt = sample
				} else {
					st.rtt = (7*st.rtt + sample) / 8
				}
			}
			delete(st.inflight, sa)
		}
	}
	st.mu.Unlock()
	st.signal() // 窗口腾出，唤醒写侧
}

// retransmit 重发超过 RTO 的分片；连续失败过多则判会话死亡。
func (s *Session) retransmit() {
	now := time.Now()
	s.mu.Lock()
	streams := make([]*stream, 0, len(s.streams))
	for _, st := range s.streams {
		streams = append(streams, st)
	}
	s.mu.Unlock()
	for _, st := range streams {
		st.mu.Lock()
		dead := false
		for seq, seg := range st.inflight {
			if now.Sub(seg.sent) < seg.lastRTO {
				continue
			}
			seg.tries++
			seg.lastRTO *= 2
			if seg.lastRTO > maxRTO {
				seg.lastRTO = maxRTO
			}
			seg.sent = now
			seg.transmit = true
			st.mu.Unlock()
			_ = s.send(wirePkt{
				conv:    s.conv,
				cmd:     cmdPSH,
				stream:  st.id,
				seq:     seq,
				ack:     st.rxWant,
				payload: seg.data,
			})
			st.mu.Lock()
		}
		st.mu.Unlock()
		if dead {
			s.dead.Store(true)
			s.closeAll(errSessionDead)
			return
		}
	}
}

func seqBefore(a, b uint32) bool { return int32(a-b) < 0 }

// ---------------------------------------------------------------------------
// Listener（服务端）：单 socket 多会话

// Listener serves reliable UDP sessions on one UDP socket.
type Listener struct {
	pc       *net.UDPConn
	mu       sync.Mutex
	sessions map[uint32]*Session
	accept   chan *Session
	closed   atomic.Bool
}

// NewListener wraps a bound UDP socket.
func NewListener(pc *net.UDPConn) *Listener {
	l := &Listener{pc: pc, sessions: map[uint32]*Session{}, accept: make(chan *Session, 8)}
	go l.readLoop()
	return l
}

func (l *Listener) readLoop() {
	buf := make([]byte, 65536)
	for {
		if l.closed.Load() {
			return
		}
		n, raddr, err := l.pc.ReadFromUDP(buf)
		if err != nil {
			if l.closed.Load() {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		p, err := decodePkt(buf[:n])
		if err != nil {
			continue
		}
		if p.cmd == cmdSYN {

		}
		l.mu.Lock()
		sess := l.sessions[p.conv]
		if sess != nil && p.cmd == cmdSYN {
			// 会话已存在：SYN 是客户端没收到上一次 SYNACK 的重发，补发一次
			_ = sess.send(wirePkt{conv: p.conv, cmd: cmdSYNACK})
		}
		if sess == nil {
			if p.cmd != cmdSYN || l.closed.Load() || len(l.sessions) >= 512 {
				l.mu.Unlock()
				continue
			}
			sess = &Session{
				pc:      l.pc,
				remote:  raddr,
				conv:    p.conv,
				isServ:  true,
				streams: map[uint8]*stream{},
				pkts:    make(chan wirePkt, 512),
				onNewFn: func(s *Session, id uint8) {},
			}
			l.sessions[p.conv] = sess
			_ = sess.send(wirePkt{conv: p.conv, cmd: cmdSYNACK})
			sess.wg.Add(1)
			go sess.pump()
			l.accept <- sess
			l.mu.Unlock()
			continue
		}
		l.mu.Unlock()
		select {
		case sess.pkts <- p:
		default: // 会话处理过慢，丢弃数据报（可靠层会重传）
		}
	}
}

// AcceptSession waits for a new client session.
func (l *Listener) AcceptSession() (*Session, error) {
	for {
		if l.closed.Load() {
			return nil, errors.New("udpstream: listener closed")
		}
		select {
		case s := <-l.accept:
			if s.dead.Load() {
				continue
			}
			return s, nil
		case <-time.After(200 * time.Millisecond):
			if l.closed.Load() {
				return nil, errors.New("udpstream: listener closed")
			}
		}
	}
}

// Close shuts the listener down.
func (l *Listener) Close() error {
	if l.closed.Swap(true) {
		return nil
	}
	return l.pc.Close()
}

// ---------------------------------------------------------------------------
// Dial（客户端）

// Dial establishes a client session to a KCP server.
func Dial(remote *net.UDPAddr, timeout time.Duration) (*Session, error) {
	if timeout <= 0 {
		timeout = synTimeout
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	conv := randomUint32()
	s := &Session{
		pc:      pc,
		remote:  remote,
		conv:    conv,
		streams: map[uint8]*stream{},
		pkts:    make(chan wirePkt, 512),
	}
	deadline := time.Now().Add(timeout)
	syn := wirePkt{conv: conv, cmd: cmdSYN}
	retry := time.NewTicker(synRetryEvery)
	defer retry.Stop()
	for {
		if err := s.send(syn); err != nil {
			_ = s.Close()
			return nil, err
		}
		// 同步读 socket，等 SYNACK（重传窗口内）
		pc.SetReadDeadline(time.Now().Add(synRetryEvery + 200*time.Millisecond))
		buf := make([]byte, 65536)
		n, _, err := pc.ReadFromUDP(buf)
		if err != nil {
			if time.Now().After(deadline) {
				_ = s.Close()
				return nil, fmt.Errorf("udpstream: SYN timeout to %s", remote)
			}
			continue // 读超时：重发 SYN
		}
		p, err := decodePkt(buf[:n])
		if err != nil || p.cmd != cmdSYNACK || p.conv != conv {
			continue
		}
		break // 握手完成
	}
	// 清除握手期设置的读超时，否则后台读取循环会立刻超时退出
	_ = pc.SetReadDeadline(time.Time{})

	// 握手完成：启动会话泵与 socket 读取循环
	s.wg.Add(2)
	go s.pump()
	go func() {
		defer s.wg.Done()
		buf := make([]byte, 65536)
		for {
			if s.dead.Load() || s.closed.Load() {
				return
			}
			n, _, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			p, err := decodePkt(buf[:n])
			if err != nil {
				continue
			}
			select {
			case s.pkts <- p:
			default: // 泵处理过慢时丢弃，由重传兜底
			}
		}
	}()
	return s, nil
}

var _ = os.Getenv    // 保持导入集稳定
var _ = strconv.Itoa // 同上
var _ = fmt.Sprintf  // 同上
