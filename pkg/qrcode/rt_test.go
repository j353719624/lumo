package qrcode

import (
	"bytes"
	"testing"
)

// full decode-style round trip: read format mask from the produced matrix,
// unmask, collect codewords in placement order, deinterleave and compare
// against the expected byte-mode stream.
func TestRoundTrip(t *testing.T) {
	data := "otpauth://totp/LUMO:admin?secret=S5JYU6ZOIQ7XVIYH&issuer=LUMO&digits=6&period=30"
	m := Encode(data, M)
	if m == nil {
		t.Fatal("nil matrix")
	}
	n := len(m)
	ver := (n - 17) / 4

	// reserved grid mirror
	reserved := make([][]bool, n)
	for i := range reserved {
		reserved[i] = make([]bool, n)
	}
	set := func(r, c int) {
		if r >= 0 && r < n && c >= 0 && c < n {
			reserved[r][c] = true
		}
	}
	for _, fr := range [][2]int{{0, 0}, {0, n - 7}, {n - 7, 0}} {
		for dr := -1; dr <= 7; dr++ {
			for dc := -1; dc <= 7; dc++ {
				set(fr[0]+dr, fr[1]+dc)
			}
		}
	}
	for i := 8; i < n-8; i++ {
		set(6, i)
		set(i, 6)
	}
	if ver >= 2 {
		for _, rr := range alignCenters[ver-2] {
			for _, cc := range alignCenters[ver-2] {
				if rr == 6 && cc == 6 || rr == 6 && cc == n-7 || rr == n-7 && cc == 6 {
					continue
				}
				for dr := -2; dr <= 2; dr++ {
					for dc := -2; dc <= 2; dc++ {
						set(rr+dr, cc+dc)
					}
				}
			}
		}
	}
	set(n-8, 8)
	for i := 0; i <= 8; i++ {
		if i != 6 {
			set(8, i)
			set(i, 8)
		}
	}
	for i := 0; i < 8; i++ {
		set(8, n-1-i)
		set(n-1-i, 8)
	}

	// read format info back (same layout formatBits writes)
	bitAt := func(i int) bool {
		switch {
		case i <= 5:
			return m[i][8]
		case i == 6:
			return m[7][8]
		case i == 7:
			return m[8][8]
		case i == 8:
			return m[8][7]
		default:
			return m[8][14-i]
		}
	}
	var fmtRaw int
	for i := 0; i < 15; i++ {
		if bitAt(i) {
			fmtRaw |= 1 << uint(i)
		}
	}
	fmtBits := fmtRaw ^ 0x5412
	data5 := fmtBits >> 10
	mask := data5 & 7
	ecc := (data5 >> 3) & 3
	if ecc != eccBits(M) {
		t.Fatalf("format info ECC bits = %d, want %d (raw %x)", ecc, eccBits(M), fmtRaw)
	}
	if mask < 0 || mask > 7 {
		t.Fatalf("bad mask %d", mask)
	}

	// unmask + collect bits in placement order
	var bits []bool
	up := true
	for col := n - 1; col > 0; col -= 2 {
		if col == 6 {
			col--
		}
		for i := 0; i < n; i++ {
			r := i
			if up {
				r = n - 1 - i
			}
			for _, c := range []int{col, col - 1} {
				if reserved[r][c] {
					continue
				}
				v := m[r][c]
				if maskBit(mask, r, c) {
					v = !v
				}
				bits = append(bits, v)
			}
		}
		up = !up
	}

	blocks := blockTable[M][ver-1]
	totalData := 0
	for _, b := range blocks {
		totalData += b.dataCW * b.numBlocks
	}
	if len(bits) < totalData*8 {
		t.Fatalf("only %d data positions, need %d", len(bits), totalData*8)
	}
	codewords := make([]byte, totalData)
	for i := range codewords {
		for k := 0; k < 8; k++ {
			codewords[i] = codewords[i]<<1 | b2i(bits[i*8+k])
		}
	}

	// deinterleave into blocks
	nBlocks := 0
	for _, b := range blocks {
		nBlocks += b.numBlocks
	}
	dataCW := make([][]byte, nBlocks)
	{
	 lens := make([]int, nBlocks)
	 pos := 0
	 bi := 0
	 for _, b := range blocks {
		 for k := 0; k < b.numBlocks; k++ {
			 lens[bi] = b.dataCW
			 bi++
		 }
	 }
	 for i := 0; i < lens[0]; i++ {
		 for b := 0; b < nBlocks; b++ {
			 if i < lens[b] {
				 dataCW[b] = append(dataCW[b], codewords[pos])
				 pos++
			 }
		 }
	 }
	}

	// re-interleave the blocks and compare with the contiguous expected
	// stream: mode 0100 | 8-bit length | payload bytes
	var joined []byte
	for i := 0; ; i++ {
		ended := true
		for b := 0; b < nBlocks; b++ {
			if i < len(dataCW[b]) {
				joined = append(joined, dataCW[b][i])
				ended = false
			}
		}
		if ended {
			break
		}
	}
	// expected bit stream: mode 0100 | 8-bit length | payload, packed MSB-first
	var expect bitWriter
	expect.write(4, 4)
	expect.write(len(data), 8)
	for i := 0; i < len(data); i++ {
		expect.write(int(data[i]), 8)
	}
	expect.write(0, 4)
	for expect.nbits%8 != 0 && expect.nbits < totalData*8 {
		expect.write(0, 1)
	}
	for expect.nbits < totalData*8 {
		expect.write(0xEC, 8)
		if expect.nbits < totalData*8 {
			expect.write(0x11, 8)
		}
	}
	// split into blocks and interleave, exactly like Encode
	pos := 0
	var expBlocks [][]byte
	for _, b := range blocks {
		for k := 0; k < b.numBlocks; k++ {
			expBlocks = append(expBlocks, expect.buf[pos:pos+b.dataCW])
			pos += b.dataCW
		}
	}
	var wantStream []byte
	for i := 0; ; i++ {
		ended := true
		for _, b := range expBlocks {
			if i < len(b) {
				wantStream = append(wantStream, b[i])
				ended = false
			}
		}
		if ended {
			break
		}
	}
	if !bytes.Equal(joined, wantStream) {
		t.Fatalf("stream mismatch:\n got  % x\n want % x", joined[:16], wantStream[:16])
	}
}

func b2i(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// verify the ECC codewords carried in the matrix equal rsEncode of the
// data blocks (this is what decoders check before accepting the payload).
func TestECCMatch(t *testing.T) {
	data := "otpauth://totp/LUMO:admin?secret=S5JYU6ZOIQ7XVIYH&issuer=LUMO&digits=6&period=30"
	m := Encode(data, M)
	n := len(m)
	ver := (n - 17) / 4

	reserved := make([][]bool, n)
	for i := range reserved {
		reserved[i] = make([]bool, n)
	}
	set := func(r, c int) {
		if r >= 0 && r < n && c >= 0 && c < n {
			reserved[r][c] = true
		}
	}
	for _, fr := range [][2]int{{0, 0}, {0, n - 7}, {n - 7, 0}} {
		for dr := -1; dr <= 7; dr++ {
			for dc := -1; dc <= 7; dc++ {
				set(fr[0]+dr, fr[1]+dc)
			}
		}
	}
	for i := 8; i < n-8; i++ {
		set(6, i)
		set(i, 6)
	}
	if ver >= 2 {
		for _, rr := range alignCenters[ver-2] {
			for _, cc := range alignCenters[ver-2] {
				if rr == 6 && cc == 6 || rr == 6 && cc == n-7 || rr == n-7 && cc == 6 {
					continue
				}
				for dr := -2; dr <= 2; dr++ {
					for dc := -2; dc <= 2; dc++ {
						set(rr+dr, cc+dc)
					}
				}
			}
		}
	}
	set(n-8, 8)
	for i := 0; i <= 8; i++ {
		if i != 6 {
			set(8, i)
			set(i, 8)
		}
	}
	for i := 0; i < 8; i++ {
		set(8, n-1-i)
		set(n-1-i, 8)
	}

	bitAt := func(i int) bool {
		switch {
		case i <= 5:
			return m[i][8]
		case i == 6:
			return m[7][8]
		case i == 7:
			return m[8][8]
		case i == 8:
			return m[8][7]
		default:
			return m[8][14-i]
		}
	}
	var fmtRaw int
	for i := 0; i < 15; i++ {
		if bitAt(i) {
			fmtRaw |= 1 << uint(i)
		}
	}
	data5 := (fmtRaw ^ 0x5412) >> 10
	mask := data5 & 7

	var bits []bool
	up := true
	for col := n - 1; col > 0; col -= 2 {
		if col == 6 {
			col--
		}
		for i := 0; i < n; i++ {
			r := i
			if up {
				r = n - 1 - i
			}
			for _, c := range []int{col, col - 1} {
				if reserved[r][c] {
					continue
				}
				v := m[r][c]
				if maskBit(mask, r, c) {
					v = !v
				}
				bits = append(bits, v)
			}
		}
		up = !up
	}

	blocks := blockTable[M][ver-1]
	totalData, totalEC := 0, 0
	for _, b := range blocks {
		totalData += b.dataCW * b.numBlocks
		totalEC += (b.totalCW - b.dataCW) * b.numBlocks
	}
	cw := func(off int) byte {
		var v byte
		for k := 0; k < 8; k++ {
			v = v<<1 | b2i(bits[off*8+k])
		}
		return v
	}
	// deinterleave data and EC
	dataBlk, ecBlk := [][]byte{}, [][]byte{}
	pos := 0
	first := true
	for _, b := range blocks {
		for k := 0; k < b.numBlocks; k++ {
			dataBlk = append(dataBlk, nil)
			ecBlk = append(ecBlk, nil)
		}
		_ = first
		first = false
	}
	for i := 0; i < blocks[0].dataCW; i++ {
		idx := 0
		for _, b := range blocks {
			for k := 0; k < b.numBlocks; k++ {
				if i < b.dataCW {
					dataBlk[idx] = append(dataBlk[idx], cw(pos))
					pos++
				}
				idx++
			}
		}
	}
	ecLen := blocks[0].totalCW - blocks[0].dataCW
	for i := 0; i < ecLen; i++ {
		idx := 0
		for _, b := range blocks {
			for k := 0; k < b.numBlocks; k++ {
				if i < b.totalCW-b.dataCW {
					ecBlk[idx] = append(ecBlk[idx], cw(pos))
					pos++
				}
				idx++
			}
		}
	}
	_ = totalData
	_ = totalEC
	for bi := range dataBlk {
		want := rsEncode(dataBlk[bi], ecLen)
		for i := range want {
			if want[i] != ecBlk[bi][i] {
				t.Fatalf("block %d EC byte %d: matrix %x != computed %x", bi, i, ecBlk[bi][i], want[i])
			}
		}
	}
	t.Logf("ECC matches for %d blocks", len(dataBlk))
}
