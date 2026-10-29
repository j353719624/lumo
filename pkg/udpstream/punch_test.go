package udpstream

import (
	"crypto/rand"
	"net"
	"testing"
	"time"
)

// TestPunchedSession verifies the Adopt/DialOn handshake over two plain UDP
// sockets (the shape a STUN punch leaves behind) and a stream round trip.
func TestPunchedSession(t *testing.T) {
	pc1, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	pc2, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer pc1.Close()
	defer pc2.Close()
	addr1 := pc1.LocalAddr().(*net.UDPAddr)
	addr2 := pc2.LocalAddr().(*net.UDPAddr)
	conv := uint32(0xabcdef01)

	// 服务侧（Adopt）与拨号侧（DialOn）同时打洞
	serverCh := make(chan *Session, 1)
	go func() { serverCh <- Adopt(pc1, addr2, conv) }()
	client, err := DialOn(pc2, addr1, conv, 4*time.Second)
	if err != nil {
		t.Fatalf("dial on punched socket: %v", err)
	}
	defer client.Close()
	server := <-serverCh
	defer server.Close()

	go func() { server.Punch(5, 100*time.Millisecond) }()
	// 双方各自发 SYN 的窗口内完成握手
	time.Sleep(200 * time.Millisecond)

	got := make(chan []byte, 1)
	go func() {
		st, err := server.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		buf := make([]byte, 5)
		n, err := st.Read(buf)
		if err != nil {
			t.Error(err)
			return
		}
		got <- buf[:n]
	}()
	st, err := client.Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	payload := make([]byte, 5)
	_, _ = rand.Read(payload)
	if _, err := st.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case b := <-got:
		if string(b) != string(payload) {
			t.Fatalf("payload mismatch %x vs %x", b, payload)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("server never received the stream")
	}
}
