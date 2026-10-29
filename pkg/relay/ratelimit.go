package relay

import (
	"io"
	"net"
	"sync"
	"time"
)

// bucket is a simple token bucket shared by both directions of one proxy.
type bucket struct {
	mu     sync.Mutex
	rate   float64 // bytes per second
	tokens float64
	last   time.Time
}

func newBucket(rate int64) *bucket {
	return &bucket{rate: float64(rate), tokens: float64(rate), last: time.Now()}
}

func (b *bucket) take(n int) {
	b.mu.Lock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.rate {
		b.tokens = b.rate
	}
	b.last = now
	b.tokens -= float64(n)
	var wait time.Duration
	if b.tokens < 0 {
		wait = time.Duration(-b.tokens / b.rate * float64(time.Second))
	}
	b.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}

type limitReader struct {
	r io.Reader
	b *bucket
}

func (l limitReader) Read(p []byte) (int, error) {
	if len(p) > 32*1024 {
		p = p[:32*1024]
	}
	n, err := l.r.Read(p)
	if n > 0 {
		l.b.take(n)
	}
	return n, err
}

type limitWriter struct {
	w io.Writer
	b *bucket
}

func (l limitWriter) Write(p []byte) (int, error) {
	if len(p) > 32*1024 {
		p = p[:32*1024]
	}
	l.b.take(len(p))
	return l.w.Write(p)
}

// LimitedPipe behaves like Pipe but caps the combined throughput of both
// directions at bytesPerSec. bytesPerSec <= 0 means unlimited.
func LimitedPipe(a, b net.Conn, c *Counters, bytesPerSec int64) {
	if bytesPerSec <= 0 {
		Pipe(a, b, c)
		return
	}
	if c == nil {
		c = &Counters{}
	}
	bkt := newBucket(bytesPerSec)
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(limitWriter{countWriter{b, &c.In}, bkt}, limitReader{countReader{a, &c.In}, bkt})
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(limitWriter{countWriter{a, &c.Out}, bkt}, limitReader{countReader{b, &c.Out}, bkt})
		done <- struct{}{}
	}()
	<-done
	_ = a.Close()
	_ = b.Close()
}

// LimitConn wraps a net.Conn so that both directions share a byte rate cap.
func LimitConn(c net.Conn, bytesPerSec int64) net.Conn {
	if bytesPerSec <= 0 {
		return c
	}
	bkt := newBucket(bytesPerSec)
	return &limitedConn{Conn: c, b: bkt}
}

type limitedConn struct {
	net.Conn
	b *bucket
}

func (c *limitedConn) Read(p []byte) (int, error) {
	if len(p) > 32*1024 {
		p = p[:32*1024]
	}
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.b.take(n)
	}
	return n, err
}

func (c *limitedConn) Write(p []byte) (int, error) {
	if len(p) > 32*1024 {
		p = p[:32*1024]
	}
	c.b.take(len(p))
	return c.Conn.Write(p)
}
