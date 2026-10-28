package proto

import (
	"bytes"
	"testing"
)

func TestFrameRoundtrip(t *testing.T) {
	msg := Msg{"type": "reg", "name": "web", "remote_port": 6022, "domains": []string{"a.com"}}
	var buf bytes.Buffer
	if err := Send(&buf, msg); err != nil {
		t.Fatal(err)
	}
	got, err := Recv(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Str("type") != "reg" || got.Str("name") != "web" || got.Int("remote_port") != 6022 {
		t.Errorf("roundtrip mismatch: %v", got)
	}
	if got.StrList("domains")[0] != "a.com" {
		t.Errorf("domains mismatch: %v", got.StrList("domains"))
	}
}

func TestFrameTooLarge(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	if _, err := ReadFrame(&buf); err == nil {
		t.Error("expected error for oversized frame")
	}
}

func TestPacketRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	hdr, _ := jsonMarshal(map[string]string{"raddr": "10.0.0.1:5353"})
	if err := WritePacket(&buf, hdr, []byte("payload-bytes")); err != nil {
		t.Fatal(err)
	}
	m, payload, err := ReadPacket(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if m["raddr"] != "10.0.0.1:5353" || string(payload) != "payload-bytes" {
		t.Errorf("packet mismatch: %v %q", m, payload)
	}
}

func TestSignIsDeterministicAndBoundToNonce(t *testing.T) {
	a := Sign("token", []byte("nonce-1"))
	b := Sign("token", []byte("nonce-1"))
	c := Sign("token", []byte("nonce-2"))
	if a != b {
		t.Error("same inputs should produce same signature")
	}
	if a == c {
		t.Error("different nonce must change signature")
	}
	if a == Sign("other", []byte("nonce-1")) {
		t.Error("different token must change signature")
	}
}
