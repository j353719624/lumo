package server

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"lumo/internal/log"
	"lumo/internal/relay"
)

// runVhost serves http (routed by Host header) or https (routed by SNI)
// on the configured vhost port. Traffic is routed at the raw connection
// level so clients keep full control of the protocol stream.
func (s *Server) runVhost(kind string) {
	port := s.cfg.VhostHTTPPort
	if kind == "https" {
		port = s.cfg.VhostHTTPSPort
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Errorf("vhost %s listen :%d: %v", kind, port, err)
		return
	}
	go func() {
		<-s.closedChan()
		_ = ln.Close()
	}()
	log.Infof("vhost %s listening on :%d", kind, port)
	for {
		c, err := ln.Accept()
		if err != nil {
			if s.closed.Load() {
				return
			}
			log.Errorf("vhost %s accept: %v", kind, err)
			return
		}
		go s.routeVhost(kind, c)
	}
}

func (s *Server) closedChan() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		for !s.closed.Load() {
			time.Sleep(200 * time.Millisecond)
		}
		close(ch)
	}()
	return ch
}

func (s *Server) routeVhost(kind string, c net.Conn) {
	defer c.Close()
	s.connsNow.Add(1)
	defer s.connsNow.Add(-1)

	peeked, head, err := peekHead(c)
	if err != nil {
		return
	}
	var host string
	if kind == "http" {
		host = hostFromHTTPHead(head)
		if host == "" {
			writeHTTPError(c, 400, "missing Host header")
			return
		}
	} else {
		host = sniFromClientHello(head)
		if host == "" {
			return
		}
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))

	table := s.httpDom
	if kind == "https" {
		table = s.httpsDom
	}
	s.mu.Lock()
	ent := table[host]
	s.mu.Unlock()
	if ent == nil || ent.dead.Load() {
		if kind == "http" {
			writeHTTPError(c, 404, "no proxy for host "+host)
		}
		return
	}
	wc, err := s.getWork(ent, 10*time.Second)
	if err != nil {
		if kind == "http" {
			writeHTTPError(c, 502, "proxy offline")
		}
		return
	}
	relay.Pipe(peeked, wc, &ent.cnt)
}

// peekHead reads just enough bytes to identify the target host, then
// returns a connection that replays the peeked bytes transparently.
func peekHead(c net.Conn) (net.Conn, []byte, error) {
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var head []byte
	tmp := make([]byte, 4096)
	for len(head) < 32*1024 {
		n, err := c.Read(tmp)
		head = append(head, tmp[:n]...)
		if err != nil {
			if len(head) > 0 && isCompleteHead(head) {
				break
			}
			return nil, nil, err
		}
		if isCompleteHead(head) {
			break
		}
	}
	_ = c.SetReadDeadline(time.Time{})
	return &bufferedConn{Conn: c, pre: head}, head, nil
}

func isCompleteHead(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if b[0] == 0x16 { // TLS handshake record
		return len(b) >= 5 && len(b) >= 5+int(b[3])<<8|int(b[4])
	}
	return bytes.Contains(b, []byte("\r\n\r\n"))
}

// bufferedConn replays a prefix that was already consumed while peeking.
type bufferedConn struct {
	net.Conn
	pre []byte
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if len(c.pre) > 0 {
		n := copy(p, c.pre)
		c.pre = c.pre[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// hostFromHTTPHead extracts the Host header from a peeked request head.
func hostFromHTTPHead(head []byte) string {
	lines := strings.Split(string(head), "\r\n")
	for i, line := range lines {
		if i == 0 {
			continue // request line
		}
		if line == "" {
			break // end of headers
		}
		if idx := strings.IndexByte(line, ':'); idx > 0 &&
			strings.EqualFold(strings.TrimSpace(line[:idx]), "host") {
			return strings.TrimSpace(line[idx+1:])
		}
	}
	return ""
}

func writeHTTPError(c net.Conn, code int, msg string) {
	status := fmt.Sprintf("%d %s", code, httpStatusText(code))
	body := fmt.Sprintf("<html><head><title>LUMO</title></head><body><h1>%d %s</h1><p>%s</p><hr>LUMO</body></html>",
		code, httpStatusText(code), msg)
	resp := fmt.Sprintf("HTTP/1.1 %s\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, len(body), body)
	_, _ = io.WriteString(c, resp)
}

func httpStatusText(code int) string {
	switch code {
	case 400:
		return "Bad Request"
	case 404:
		return "Not Found"
	case 502:
		return "Bad Gateway"
	default:
		return "Error"
	}
}

// sniFromClientHello parses the SNI hostname out of a TLS ClientHello.
// It returns "" for malformed or SNI-less handshakes.
func sniFromClientHello(b []byte) string {
	if len(b) < 5 || b[0] != 0x16 {
		return ""
	}
	recLen := int(b[3])<<8 | int(b[4])
	if len(b) < 5+recLen {
		return ""
	}
	hs := b[5 : 5+recLen]
	if len(hs) < 4 || hs[0] != 0x01 {
		return ""
	}
	body := hs[4:]
	p := 2 // client version
	if len(body) < p+32+1 {
		return ""
	}
	p += 32 // random
	sl := int(body[p])
	p++
	if len(body) < p+sl+2 {
		return ""
	}
	p += sl // session id
	cs := int(body[p])<<8 | int(body[p+1])
	p += 2
	if len(body) < p+cs+1 {
		return ""
	}
	p += cs // cipher suites
	cm := int(body[p])
	p++
	if len(body) < p+cm+2 {
		return ""
	}
	p += cm // compression methods
	if len(body) < p+2 {
		return ""
	}
	el := int(body[p])<<8 | int(body[p+1])
	p += 2
	end := p + el
	if end > len(body) {
		end = len(body)
	}
	for p+4 <= end {
		extType := int(body[p])<<8 | int(body[p+1])
		extLen := int(body[p+2])<<8 | int(body[p+3])
		p += 4
		if p+extLen > end {
			return ""
		}
		if extType != 0 { // server_name extension
			p += extLen
			continue
		}
		ext := body[p : p+extLen]
		if len(ext) < 2 {
			return ""
		}
		listLen := int(ext[0])<<8 | int(ext[1])
		if 2+listLen > len(ext) {
			return ""
		}
		q := 2
		for q+3 <= 2+listLen {
			nameType := ext[q]
			nameLen := int(ext[q+1])<<8 | int(ext[q+2])
			q += 3
			if q+nameLen > 2+listLen {
				return ""
			}
			if nameType == 0 {
				return string(ext[q : q+nameLen])
			}
			q += nameLen
		}
		return ""
	}
	return ""
}
