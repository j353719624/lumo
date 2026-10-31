package udpstream

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// 回收回归：流 ID 必须在流结束后回收复用，否则隧道跑满 250 条连接
// 之后永久瘫痪（线上事故：udpstream: no free stream ids）。

func pair(t *testing.T) (client, server *Session, stop func()) {
	t.Helper()
	serverPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	ln := NewListener(serverPC)

	cl, err := Dial(serverPC.LocalAddr().(*net.UDPAddr), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	srvStop := make(chan struct{})
	go func() {
		for {
			sess, err := ln.AcceptSession()
			if err != nil {
				return
			}
			go func(s *Session) {
				for {
					conn, err := s.Accept()
					if err != nil {
						return
					}
					go func(c net.Conn) {
						io.Copy(c, c)
						c.Close()
					}(conn)
				}
			}(sess)
		}
	}()
	return cl, nil, func() {
		close(srvStop)
		cl.Close()
		ln.Close()
		serverPC.Close()
	}
}

// 回声服务器上连续开 600 条流：超过 250 的旧上限，验证 ID 已回收。
func TestStreamIdsRecycleAfterClose(t *testing.T) {
	orig := reclaimCooldown
	reclaimCooldown = 100 * time.Millisecond
	defer func() { reclaimCooldown = orig }()

	cl, _, stop := pair(t)
	defer stop()

	payload := bytes.Repeat([]byte("x"), 4096)
	for i := 0; i < 600; i++ {
		conn, err := cl.Open()
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		if _, err := conn.Write(payload); err != nil {
			t.Fatalf("stream %d write: %v", i, err)
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("stream %d read: %v", i, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("stream %d: echo mismatch", i)
		}
		conn.Close()
	}
}

// 耗尽 → 报错；全部关闭并经过冷却期后 → ID 回收，可以再开。
func TestOpenExhaustionThenCooldownReclaim(t *testing.T) {
	orig := reclaimCooldown
	reclaimCooldown = 300 * time.Millisecond
	defer func() { reclaimCooldown = orig }()

	cl, _, stop := pair(t)
	defer stop()

	// Open 可用的 ID 是 1..254（255 保留），全部占满
	var conns []net.Conn
	for i := 0; i < 254; i++ {
		conn, err := cl.Open()
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		conns = append(conns, conn)
	}
	if _, err := cl.Open(); err == nil {
		t.Fatal("open past exhaustion should fail")
	}
	// 不做任何读写直接硬关闭：RST 让对端也回收
	for _, c := range conns {
		c.Close()
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := cl.Open(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("ids were not reclaimed after close + cooldown")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
