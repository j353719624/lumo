package udpstream

import (
	"net"
	"testing"
	"time"
)

// 无 shim 直连：隔离传输层本身的问题
func TestDirectDial(t *testing.T) {
	serverPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer serverPC.Close()
	serverAddr := serverPC.LocalAddr().(*net.UDPAddr)

	listener := NewListener(serverPC)
	go func() {
		for {
			s, err := listener.AcceptSession()
			if err != nil {
				return
			}
			t.Log("session accepted, conv:", s.conv)
		}
	}()

	sess, err := Dial(serverAddr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := sess.Open()
	if err != nil {
		t.Fatal(err)
	}
	go conn.Write([]byte("ping"))

	time.Sleep(500 * time.Millisecond) // 给服务端处理时间
}
