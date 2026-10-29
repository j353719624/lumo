package server

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/j353719624/lumo/pkg/log"
	"github.com/j353719624/lumo/pkg/relay"
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
	list := append([]*proxyEnt(nil), table[host]...)
	s.mu.Unlock()

	path := "/"
	if kind == "http" {
		path = pathFromHTTPHead(head)
	}
	var ent *proxyEnt
	best := -1
	for _, e := range list {
		if e == nil || e.dead.Load() {
			continue
		}
		if score := e.matchSubPath(path); score > best {
			best = score
			ent = e
		}
	}
	if ent == nil {
		if kind == "http" {
			writeHTTPError(c, 404, "no proxy for host "+host)
		}
		return
	}
	// HTTP 入口的 Basic 认证（TLS 直通无法检查明文请求头，仅 http 代理生效）
	if kind == "http" && ent.httpUser != "" {
		user, pass, ok := basicAuthFromHead(head)
		okUser := subtle.ConstantTimeCompare([]byte(user), []byte(ent.httpUser)) == 1
		okPass := subtle.ConstantTimeCompare([]byte(pass), []byte(ent.httpPass)) == 1
		if !ok || !okUser || !okPass {
			writeHTTPAuthRequired(c)
			return
		}
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

// matchSubPath reports how specifically this proxy matches the request path.
// A negative score means "no match"; "/" (the default) matches everything.
func (e *proxyEnt) matchSubPath(path string) int {
	if len(e.subPaths) == 0 {
		return 0
	}
	best := -1
	for _, sp := range e.subPaths {
		sp = strings.TrimSpace(sp)
		if sp == "" {
			sp = "/"
		}
		if !strings.HasPrefix(sp, "/") {
			sp = "/" + sp
		}
		if sp == "/" {
			if best < 0 {
				best = 0
			}
			continue
		}
		if path == sp || strings.HasPrefix(path, sp+"/") || strings.HasPrefix(path, sp+"?") {
			if len(sp) > best {
				best = len(sp)
			}
		}
	}
	return best
}

// pathFromHTTPHead extracts the request path from the first request line.
func pathFromHTTPHead(head []byte) string {
	line := string(head)
	if i := strings.Index(line, "\r\n"); i > 0 {
		line = line[:i]
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return "/"
	}
	target := parts[1]
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		if i := strings.Index(target, "://"); i > 0 {
			rest := target[i+3:]
			if j := strings.IndexByte(rest, '/'); j >= 0 {
				target = rest[j:]
			} else {
				target = "/"
			}
		}
	}
	if i := strings.IndexByte(target, '?'); i >= 0 {
		target = target[:i]
	}
	if target == "" {
		return "/"
	}
	return target
}

// basicAuthFromHead parses an Authorization: Basic header.
func basicAuthFromHead(head []byte) (user, pass string, ok bool) {
	for _, line := range strings.Split(string(head), "\r\n")[1:] {
		if line == "" {
			break
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 || !strings.EqualFold(strings.TrimSpace(line[:idx]), "authorization") {
			continue
		}
		val := strings.TrimSpace(line[idx+1:])
		if !strings.HasPrefix(strings.ToLower(val), "basic ") {
			return "", "", false
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(val[6:]))
		if err != nil {
			return "", "", false
		}
		u, p, found := strings.Cut(string(raw), ":")
		return u, p, found
	}
	return "", "", false
}

// writeHTTPAuthRequired replies 401 with a Basic challenge.
func writeHTTPAuthRequired(c net.Conn) {
	body := "<html><head><title>LUMO</title></head><body><h1>401 Unauthorized</h1><hr>LUMO</body></html>"
	resp := fmt.Sprintf("HTTP/1.1 401 Unauthorized\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\nWWW-Authenticate: Basic realm=\"LUMO\"\r\nConnection: close\r\n\r\n%s",
		len(body), body)
	_, _ = io.WriteString(c, resp)
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
