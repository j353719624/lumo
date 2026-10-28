package qrcode

import "testing"

// Reference vector (ISO/IEC 18004 example, v1-M "HELLO WORLD"):
// 16 data codewords -> 10 EC codewords.
func TestRSKnownVector(t *testing.T) {
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	got := rsEncode(data, 10)
	if len(got) != len(want) {
		t.Fatalf("len %d != %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EC[%d] = %d, want %d (full: %v)", i, got[i], want[i], got)
		}
	}
}
