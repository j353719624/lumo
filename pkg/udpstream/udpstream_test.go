package udpstream

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// lossyShim 模拟丢包/乱序链路：客户端地址从流量中学习，服务端方向原样转发
func lossyShim(t *testing.T, shim *net.UDPConn, serverAddr *net.UDPAddr, dropPct int) {
	t.Helper()
	var clientAddr *net.UDPAddr
	var cnt uint64
	buf := make([]byte, 65536)
	for {
		n, src, err := shim.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		var dst *net.UDPAddr
		fromServer := src.String() == serverAddr.String()
		if fromServer {
			dst = clientAddr
		} else {
			clientAddr = src
			dst = serverAddr
		}
		if dst == nil {
			continue
		}
		cnt++
		if (cnt*2654435761)%100 < uint64(dropPct) {
			continue
		}
		if _, err := shim.WriteToUDP(buf[:n], dst); err != nil {
			return
		}
	}
}

func TestReliableDeliveryWithLoss(t *testing.T) {
	dropPct := 20

	serverPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer serverPC.Close()
	serverAddr := serverPC.LocalAddr().(*net.UDPAddr)

	shim, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer shim.Close()

	// 服务端接受会话与流
	listener := NewListener(serverPC)
	sessions := make(chan *Session, 4)
	go func() {
		for {
			s, err := listener.AcceptSession()
			if err != nil {
				return
			}
			sessions <- s
			go func(sess *Session) {
				for {
					conn, err := sess.Accept()
					if err != nil {
						return
					}
					go func(c net.Conn) {
						buf := make([]byte, 65536)
						for {
							n, err := c.Read(buf)
							if err != nil {
								return
							}
							c.Write(buf[:n]) // 回显
						}
					}(conn)
				}
			}(s)
		}
	}()

	// 丢包 shim
	go lossyShim(t, shim, serverAddr, dropPct)

	// 客户端拨号
	sess, err := Dial(shim.LocalAddr().(*net.UDPAddr), 6*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.Close()

	// 三条并发流，各自写大块数据并读回显
	patterns := [][]byte{}
	for i := 0; i < 3; i++ {
		b := make([]byte, 300*1024)
		_, _ = rand.Read(b)
		for j := range b {
			b[j] = byte(j*31 + i*7)
		}
		patterns = append(patterns, b)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int, payload []byte) {
			defer wg.Done()
			conn, err := sess.Open()
			if err != nil {
				errCh <- err
				return
			}
			defer conn.Close()
			go func() {
				_, _ = conn.Write(payload)
			}()
			got := make([]byte, len(payload))
			read := 0
			for read < len(payload) {
				n, err := conn.Read(got[read:])
				if err != nil {
					errCh <- fmt.Errorf("stream %d read: %w", i, err)
					return
				}
				read += n
			}
			if !bytes.Equal(got, payload) {
				off := -1
				for k := range got {
					if got[k] != payload[k] {
						off = k
						break
					}
				}
				errCh <- fmt.Errorf("stream %d mismatch len=%d exp=%d firstDiff@%d got=%x exp=%x", i, len(got), len(payload), off, got[off:off+16], payload[off:off+16])
				return
			}
		}(i, patterns[i])
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(60 * time.Second):
		t.Fatal("timeout waiting for streams")
	}
}

func TestWireFormatRoundTrip(t *testing.T) {
	p := wirePkt{
		conv: 0xdeadbeef, cmd: cmdPSH, stream: 7, fin: true,
		seq: 0x11223344, ack: 0x55667788,
		sack:    []uint32{1, 2, 0xfffffff0},
		payload: []byte("hello"),
	}
	enc := encodePkt(p)
	got, err := decodePkt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got.conv != p.conv || got.cmd != p.cmd || got.stream != p.stream ||
		got.fin != p.fin || got.seq != p.seq || got.ack != p.ack ||
		len(got.sack) != 3 || string(got.payload) != string(p.payload) {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestShortDatagram(t *testing.T) {
	if _, err := decodePkt([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for short datagram")
	}
}
