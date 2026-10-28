// Package relay provides bidirectional connection splicing with traffic
// accounting, shared by the server and the client.
package relay

import (
	"io"
	"net"
	"sync/atomic"
)

// Counters accumulates transferred bytes. In counts traffic arriving from
// the public side (visitors), Out counts traffic sent back to it.
type Counters struct {
	In  atomic.Uint64
	Out atomic.Uint64
}

type countReader struct {
	r io.Reader
	n *atomic.Uint64
}

func (c countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(uint64(n))
	return n, err
}

type countWriter struct {
	w io.Writer
	n *atomic.Uint64
}

func (c countWriter) Write(p []byte) (int, error) {
	c.n.Add(uint64(len(p)))
	return c.w.Write(p)
}

// Conn wraps a net.Conn and counts bytes in one direction.
type Conn struct {
	net.Conn
	C  *Counters
	In bool
}

func (c *Conn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if c.In {
		c.C.In.Add(uint64(n))
	} else {
		c.C.Out.Add(uint64(n))
	}
	return n, err
}

func (c *Conn) Write(p []byte) (int, error) {
	if c.In {
		c.C.Out.Add(uint64(len(p)))
	} else {
		c.C.In.Add(uint64(len(p)))
	}
	return c.Conn.Write(p)
}

// Pipe splices a and b together until either side closes. a is the public
// side, b the service side. Both connections are closed on return. A nil
// counter set disables accounting, which keeps visitor paths allocation-free.
func Pipe(a, b net.Conn, c *Counters) {
	if c == nil {
		c = &Counters{}
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(countWriter{b, &c.In}, countReader{a, &c.In})
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(countWriter{a, &c.Out}, countReader{b, &c.Out})
		done <- struct{}{}
	}()
	<-done
	_ = a.Close()
	_ = b.Close()
}
