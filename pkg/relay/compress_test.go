package relay

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"
)

func TestCompressConnRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	ca := CompressConn(a)
	cb := CompressConn(b)

	payloads := [][]byte{
		[]byte("hello"),
		bytes.Repeat([]byte("lumo compress "), 5000), // highly compressible
		make([]byte, 5000),                           // incompressible zeros? (zeros compress well)
	}
	rnd := make([]byte, 3000)
	_, _ = rand.Read(rnd)
	payloads = append(payloads, rnd) // incompressible random

	go func() {
		for _, p := range payloads {
			if _, err := ca.Write(p); err != nil {
				t.Errorf("write: %v", err)
				return
			}
		}
	}()
	_ = cb.SetReadDeadline(time.Now().Add(5 * time.Second))
	var got bytes.Buffer
	want := 0
	for _, p := range payloads {
		want += len(p)
	}
	if _, err := io.CopyN(&got, cb, int64(want)); err != nil {
		t.Fatalf("read: %v", err)
	}
	var expected bytes.Buffer
	for _, p := range payloads {
		expected.Write(p)
	}
	if !bytes.Equal(got.Bytes(), expected.Bytes()) {
		t.Fatal("round trip mismatch")
	}
}
