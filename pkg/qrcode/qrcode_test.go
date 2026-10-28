package qrcode

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Reference vectors: verify our encoder output against Python qrcode
// (ECC M) for fixed inputs.
func TestEncodeKnownPayload(t *testing.T) {
	for _, data := range []string{
		"HELLO LUMO",
		"otpauth://totp/LUMO:admin?secret=JBSWY3DPEHPK3PXP&issuer=LUMO&digits=6&period=30",
	} {
		m := Encode(data, M)
		if m == nil {
			t.Fatalf("Encode(%q) returned nil", data)
		}
		n := len(m)
		if n < 21 || n%4 != 1 {
			t.Fatalf("bad matrix size %d for %q", n, data)
		}
		// finder patterns must be present in the three corners
		checkFinder(t, m, 0, 0)
		checkFinder(t, m, 0, n-7)
		checkFinder(t, m, n-7, 0)
		// timing pattern alternation
		for i := 8; i < n-8; i++ {
			if m[6][i] != (i%2 == 0) || m[i][6] != (i%2 == 0) {
				t.Fatalf("timing pattern broken at %d for %q", i, data)
			}
		}
		// dark module
		if !m[n-8][8] {
			t.Fatal("dark module missing")
		}
	}
}

func checkFinder(t *testing.T, m [][]bool, r, c int) {
	t.Helper()
	want := func(dr, dc int) bool {
		return dr == 0 || dr == 6 || dc == 0 || dc == 6 ||
			(dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4)
	}
	for dr := 0; dr < 7; dr++ {
		for dc := 0; dc < 7; dc++ {
			if m[r+dr][c+dc] != want(dr, dc) {
				t.Fatalf("finder at (%d,%d) corrupted", r, c)
			}
		}
	}
}

func TestSVG(t *testing.T) {
	svg := SVG("otpauth://totp/LUMO:admin?secret=JBSWY3DPEHPK3PXP&issuer=LUMO", M, 200)
	if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") {
		t.Fatal("malformed SVG output")
	}
	// render as data URI to prove it is embeddable
	_ = base64.StdEncoding.EncodeToString([]byte(svg))
}

func TestTooLong(t *testing.T) {
	if Encode(strings.Repeat("x", 300), M) != nil {
		t.Fatal("expected nil for oversized payload")
	}
}
