// Package stun implements just enough of RFC 5389 to learn the external
// address of a UDP socket (a Binding request to a public STUN server).
package stun

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

const magicCookie = 0x2112A442

const (
	attrMappedAddress    = 0x0001
	attrXORMappedAddress = 0x0020
)

// Discover sends one Binding request from pc to server and returns the
// public address the server observed (IPv4).
func Discover(pc *net.UDPConn, server string, timeout time.Duration) (*net.UDPAddr, error) {
	raddr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, err
	}
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:2], 0x0001) // Binding Request
	binary.BigEndian.PutUint16(req[2:4], 0)      // length
	binary.BigEndian.PutUint32(req[4:8], magicCookie)
	txid := req[8:20]
	if _, err := rand.Read(txid); err != nil {
		return nil, err
	}
	if _, err := pc.WriteToUDP(req, raddr); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 1500)
	for time.Now().Before(deadline) {
		_ = pc.SetReadDeadline(deadline)
		n, from, err := pc.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break
			}
			return nil, err
		}
		if from.String() != raddr.String() || n < 20 {
			continue
		}
		if binary.BigEndian.Uint32(buf[4:8]) != magicCookie || string(buf[8:20]) != string(txid) {
			continue
		}
		if addr := parseMapped(buf[:n], txid); addr != nil {
			return addr, nil
		}
	}
	return nil, fmt.Errorf("stun: no binding response from %s", server)
}

func parseMapped(msg []byte, txid []byte) *net.UDPAddr {
	typ := binary.BigEndian.Uint16(msg[0:2])
	if typ != 0x0101 {
		return nil
	}
	length := int(binary.BigEndian.Uint16(msg[2:4]))
	b := msg[20:]
	if len(b) > length {
		b = b[:length]
	}
	for len(b) >= 4 {
		at := binary.BigEndian.Uint16(b[0:2])
		alen := int(binary.BigEndian.Uint16(b[2:4]))
		b = b[4:]
		if alen > len(b) {
			return nil
		}
		val := b[:alen]
		b = b[alen:]
		switch at {
		case attrMappedAddress:
			if alen >= 8 && val[1] == 0x01 {
				port := binary.BigEndian.Uint16(val[2:4])
				ip := net.IP(val[4:8])
				return &net.UDPAddr{IP: ip, Port: int(port)}
			}
		case attrXORMappedAddress:
			if alen >= 8 && val[1] == 0x01 {
				port := binary.BigEndian.Uint16(val[2:4]) ^ uint16(magicCookie>>16)
				ip := make(net.IP, 4)
				cookie := make([]byte, 4)
				binary.BigEndian.PutUint32(cookie, magicCookie)
				for i := 0; i < 4; i++ {
					ip[i] = val[4+i] ^ cookie[i]
				}
				return &net.UDPAddr{IP: ip, Port: int(port)}
			}
		}
	}
	return nil
}
