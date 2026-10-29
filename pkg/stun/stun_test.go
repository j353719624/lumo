package stun

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// TestParseXORMapped builds a synthetic Binding success response and checks
// the decoder against the RFC 5389 XOR rules.
func TestParseXORMapped(t *testing.T) {
	txid := []byte("abcdefghijkl")
	ip := net.IPv4(203, 0, 113, 7).To4()
	port := 54321
	msg := make([]byte, 20)
	binary.BigEndian.PutUint16(msg[0:2], 0x0101)
	binary.BigEndian.PutUint16(msg[2:4], 12)
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], txid)
	attr := make([]byte, 12)
	binary.BigEndian.PutUint16(attr[0:2], attrXORMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], 8)
	attr[5] = 0x01
	binary.BigEndian.PutUint16(attr[6:8], uint16(port)^uint16(magicCookie>>16))
	cookie := make([]byte, 4)
	binary.BigEndian.PutUint32(cookie, magicCookie)
	for i := 0; i < 4; i++ {
		attr[8+i] = ip[i] ^ cookie[i]
	}
	got := parseMapped(append(msg, attr...), txid)
	if got == nil {
		t.Fatal("no mapped address parsed")
	}
	if got.Port != port || !got.IP.Equal(ip) {
		t.Fatalf("got %v, want %v:%d", got, ip, port)
	}
}

// TestDiscoverAgainstPublicServer only runs when the network allows it.
func TestDiscoverAgainstPublicServer(t *testing.T) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0")})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	addr, err := Discover(pc, "stun.l.google.com:19302", 3*time.Second)
	if err != nil {
		t.Skipf("no internet STUN available: %v", err)
	}
	if addr.Port == 0 || addr.IP == nil {
		t.Fatalf("bad mapped addr %v", addr)
	}
	t.Logf("reflexive address %v", addr)
}
