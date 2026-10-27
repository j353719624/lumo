// Package proto defines the LUMO control-channel wire protocol: length
// framed JSON messages, HMAC-SHA256 token authentication and the framed
// packet format used to carry UDP datagrams over data connections.
package proto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
)

// Version is the protocol version negotiated in the handshake.
const Version = 1

// maxFrame caps control message size.
const maxFrame = 1 << 20

// WriteFrame writes one length-prefixed message.
func WriteFrame(w io.Writer, payload []byte) error {
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(payload)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadFrame reads one length-prefixed message.
func ReadFrame(r io.Reader) ([]byte, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(head[:])
	if n > maxFrame {
		return nil, fmt.Errorf("frame too large: %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// WritePacket writes a framed datagram: 4B length | 2B header length |
// header JSON | payload. Used for UDP relay over data connections.
func WritePacket(w io.Writer, header []byte, payload []byte) error {
	if len(header) > 0xFFFF {
		return fmt.Errorf("packet header too large")
	}
	total := 2 + len(header) + len(payload)
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(total))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	var hl [2]byte
	binary.BigEndian.PutUint16(hl[:], uint16(len(header)))
	if _, err := w.Write(hl[:]); err != nil {
		return err
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// ReadPacket reads one framed datagram. header is parsed as JSON into a
// generic map; the caller decodes the fields it expects.
func ReadPacket(r io.Reader) (header map[string]any, payload []byte, err error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, nil, err
	}
	total := binary.BigEndian.Uint32(head[:])
	if total < 2 || total > maxFrame {
		return nil, nil, fmt.Errorf("bad packet size: %d", total)
	}
	buf := make([]byte, total)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, nil, err
	}
	hl := binary.BigEndian.Uint16(buf[:2])
	if int(hl) > len(buf)-2 {
		return nil, nil, fmt.Errorf("bad packet header length: %d", hl)
	}
	header = map[string]any{}
	if hl > 0 {
		if err := decodeJSON(buf[2:2+hl], &header); err != nil {
			return nil, nil, fmt.Errorf("bad packet header: %w", err)
		}
	}
	return header, buf[2+hl:], nil
}

// Msg is one control message. Unknown fields are ignored, which keeps the
// protocol forward compatible.
type Msg map[string]any

func decodeJSON(b []byte, v any) error {
	return jsonUnmarshal(b, v)
}

// Send marshals and writes one control message.
func Send(w io.Writer, m Msg) error {
	b, err := jsonMarshal(m)
	if err != nil {
		return err
	}
	return WriteFrame(w, b)
}

// Recv reads and parses one control message.
func Recv(r io.Reader) (Msg, error) {
	b, err := ReadFrame(r)
	if err != nil {
		return nil, err
	}
	m := Msg{}
	if err := decodeJSON(b, &m); err != nil {
		return nil, fmt.Errorf("bad message: %w", err)
	}
	return m, nil
}

// Str returns the string value for key, or "".
func (m Msg) Str(key string) string { s, _ := m[key].(string); return s }

// Int returns the numeric value for key, or 0. JSON numbers arrive as
// float64; internally produced maps may hold int or int64.
func (m Msg) Int(key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// StrList returns the string list for key, or nil.
func (m Msg) StrList(key string) []string {
	raw, _ := m[key].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// Sign computes the hex HMAC-SHA256 of nonce under token.
func Sign(token string, nonce []byte) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(nonce)
	return hex.EncodeToString(mac.Sum(nil))
}

// RandomHex returns n random bytes hex encoded.
func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
