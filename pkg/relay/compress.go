package relay

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
)

// CompressConn wraps a tunnel connection with block-framed deflate. Each
// Write becomes one block: [u32 length][deflate bytes]. Interactive traffic
// is never held in a compressor buffer, so request/response latency stays
// flat; incompressible payloads fall back to a stored block.
func CompressConn(c net.Conn) net.Conn {
	cc := &compressConn{Conn: c}
	cc.br = bufio.NewReaderSize(c, 32*1024)
	return cc
}

type compressConn struct {
	net.Conn
	br   *bufio.Reader
	rbuf bytes.Buffer // decompressed remainder
	wmu  sync.Mutex
}

const (
	compressMinBlock = 64    // below this a block is stored raw
	compressMaxBlock = 32768 // per-block input cap
)

var flateWriters = sync.Pool{New: func() any {
	w, _ := flate.NewWriter(nil, flate.BestSpeed)
	return w
}}

func (c *compressConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > compressMaxBlock {
			n = compressMaxBlock
		}
		chunk := p[:n]
		p = p[n:]

		var body []byte
		if len(chunk) >= compressMinBlock {
			fw := flateWriters.Get().(*flate.Writer)
			var buf bytes.Buffer
			fw.Reset(&buf)
			_, _ = fw.Write(chunk)
			_ = fw.Close()
			flateWriters.Put(fw)
			if buf.Len() < len(chunk) {
				body = buf.Bytes()
			}
		}
		raw := body == nil
		if raw {
			body = chunk
		}
		var hdr [5]byte
		binary.BigEndian.PutUint32(hdr[:4], uint32(len(body)))
		if raw {
			hdr[4] = 1
		}
		if _, err := c.Conn.Write(hdr[:]); err != nil {
			return total, err
		}
		if _, err := c.Conn.Write(body); err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (c *compressConn) Read(p []byte) (int, error) {
	if c.rbuf.Len() > 0 {
		return c.rbuf.Read(p)
	}
	var hdr [5]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return 0, err
	}
	length := int(binary.BigEndian.Uint32(hdr[:4]))
	raw := hdr[4] == 1
	if length < 0 || length > compressMaxBlock*4 {
		return 0, fmt.Errorf("relay: corrupt compressed block (%d bytes)", length)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.br, body); err != nil {
		return 0, err
	}
	if raw {
		c.rbuf.Write(body)
	} else {
		fr := flate.NewReader(bytes.NewReader(body))
		if _, err := io.Copy(&c.rbuf, fr); err != nil {
			_ = fr.Close()
			return 0, err
		}
		_ = fr.Close()
	}
	return c.rbuf.Read(p)
}
