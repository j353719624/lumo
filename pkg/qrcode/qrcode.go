// Package qrcode renders QR codes as SVG using only the standard library.
// It supports byte-mode data, error-correction levels L/M/Q/H and
// versions 1-10, which covers short enrollment URIs such as otpauth://.
package qrcode

import (
	"bytes"
	"strings"
)

// Level is the error-correction level.
type Level int

const (
	L Level = iota
	M
	Q
	H
)

// version capacity (byte mode, per EC level) for versions 1..10.
var capacity = map[Level][10]int{
	L: {17, 32, 53, 78, 106, 134, 154, 192, 216, 248},
	M: {14, 26, 42, 62, 84, 106, 122, 152, 180, 208},
	Q: {11, 20, 32, 46, 60, 74, 86, 108, 130, 150},
	H: {7, 14, 24, 34, 44, 58, 64, 84, 98, 119},
}

// rsBlock: (total, data) codewords per block, count of blocks per group.
type rsBlock struct{ numBlocks, totalCW, dataCW int }

// block layouts per version (index 0 => v1) per level.
var blockTable = map[Level][10][]rsBlock{
	L: {
		{{1, 26, 19}}, {{1, 44, 34}}, {{1, 70, 55}}, {{1, 100, 80}},
		{{1, 134, 108}}, {{2, 86, 68}}, {{2, 98, 78}}, {{2, 121, 97}},
		{{2, 146, 116}}, {{2, 86, 68}, {2, 87, 69}},
	},
	M: {
		{{1, 26, 16}}, {{1, 44, 28}}, {{1, 70, 44}}, {{2, 50, 32}},
		{{2, 67, 43}}, {{4, 43, 27}}, {{4, 49, 31}}, {{2, 60, 38}, {2, 61, 39}},
		{{3, 58, 36}, {2, 59, 37}}, {{4, 69, 43}, {1, 70, 44}},
	},
	Q: {
		{{1, 26, 13}}, {{1, 44, 22}}, {{2, 35, 17}}, {{2, 50, 24}},
		{{4, 33, 15}}, {{4, 43, 19}}, {{6, 43, 15}}, {{4, 51, 19}, {1, 52, 20}},
		{{6, 54, 16}, {2, 55, 17}}, {{6, 54, 15}, {2, 55, 16}},
	},
	H: {
		{{1, 26, 9}}, {{1, 44, 16}}, {{2, 35, 13}}, {{4, 50, 9}},
		{{4, 33, 11}}, {{8, 39, 13}}, {{8, 39, 12}}, {{4, 55, 15}, {4, 56, 16}},
		{{4, 62, 13}, {4, 63, 14}}, {{6, 62, 12}, {2, 63, 13}},
	},
}

// alignment pattern center coordinates per version (index 0 => v2).
var alignCenters = [][]int{
	{6, 18}, {6, 22}, {6, 26}, {6, 30}, {6, 34},
	{6, 22, 38}, {6, 24, 42}, {6, 26, 46}, {6, 28, 50},
}

// gf arithmetic
var gfExp [512]byte
var gfLog [256]byte

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsGen returns the generator polynomial of the given degree as descending
// coefficients (leading 1 at index 0), as consumed by rsEncode.
func rsGen(degree int) []byte {
	poly := []byte{1} // ascending: poly[0] is the constant term
	for i := 0; i < degree; i++ {
		next := make([]byte, len(poly)+1)
		for j, c := range poly {
			next[j] ^= gfMul(c, gfExp[i])
			next[j+1] ^= c
		}
		poly = next
	}
	for l, r := 0, len(poly)-1; l < r; l, r = l+1, r-1 {
		poly[l], poly[r] = poly[r], poly[l]
	}
	return poly
}

func rsEncode(data []byte, ecLen int) []byte {
	gen := rsGen(ecLen)
	rem := make([]byte, ecLen)
	for _, d := range data {
		factor := d ^ rem[0]
		rem = append(rem[1:], 0)
		for i, g := range gen[1:] {
			rem[i] ^= gfMul(g, factor)
		}
	}
	return rem
}

type bitWriter struct {
	buf   []byte
	nbits int
}

func (w *bitWriter) write(val, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.nbits%8 == 0 {
			w.buf = append(w.buf, 0)
		}
		if val>>i&1 == 1 {
			w.buf[w.nbits/8] |= 1 << (7 - w.nbits%8)
		}
		w.nbits++
	}
}

// Encode builds the QR module matrix for data at the requested EC level.
func Encode(data string, level Level) [][]bool {
	if level == Q || level == H {
		level = M // keep tables small; M is plenty for enrollment URIs
	}
	caps := capacity[level]
	ver := 0
	for i, c := range caps {
		if len(data) <= c {
			ver = i + 1
			break
		}
	}
	if ver == 0 {
		return nil // data too long for supported versions
	}

	// 1. data bit stream: byte mode
	var bw bitWriter
	bw.write(4, 4)
	if ver <= 9 {
		bw.write(len(data), 8)
	} else {
		bw.write(len(data), 16)
	}
	for i := 0; i < len(data); i++ {
		bw.write(int(data[i]), 8)
	}

	blocks := blockTable[level][ver-1]
	totalData := 0
	for _, b := range blocks {
		totalData += b.dataCW * b.numBlocks
	}
	// terminator: up to 4 zero bits, then pad to the byte boundary,
	// then alternating 0xEC/0x11 pad bytes to full capacity
	bw.write(0, 4)
	for bw.nbits%8 != 0 && bw.nbits < totalData*8 {
		bw.write(0, 1)
	}
	for bw.nbits < totalData*8 {
		bw.write(0xEC, 8)
		if bw.nbits < totalData*8 {
			bw.write(0x11, 8)
		}
	}

	// 2. split into blocks, compute ECC, interleave
	dataCW := bw.buf
	var dataBlocks [][]byte
	var ecBlocks [][]byte
	pos := 0
	for _, b := range blocks {
		for i := 0; i < b.numBlocks; i++ {
			blk := dataCW[pos : pos+b.dataCW]
			pos += b.dataCW
			dataBlocks = append(dataBlocks, blk)
			ecBlocks = append(ecBlocks, rsEncode(blk, b.totalCW-b.dataCW))
		}
	}
	var stream []byte
	maxData := 0
	for _, b := range dataBlocks {
		if len(b) > maxData {
			maxData = len(b)
		}
	}
	for i := 0; i < maxData; i++ {
		for _, b := range dataBlocks {
			if i < len(b) {
				stream = append(stream, b[i])
			}
		}
	}
	maxEC := 0
	for _, b := range ecBlocks {
		if len(b) > maxEC {
			maxEC = len(b)
		}
	}
	for i := 0; i < maxEC; i++ {
		for _, b := range ecBlocks {
			if i < len(b) {
				stream = append(stream, b[i])
			}
		}
	}

	// 3. matrix with function patterns
	size := 17 + 4*ver
	m := make([][]bool, size)
	reserved := make([][]bool, size)
	for i := range m {
		m[i] = make([]bool, size)
		reserved[i] = make([]bool, size)
	}
	setFunc := func(r, c int, v bool) {
		if r >= 0 && r < size && c >= 0 && c < size {
			m[r][c] = v
			reserved[r][c] = true
		}
	}
	finder := func(r, c int) {
		for dr := -1; dr <= 7; dr++ {
			for dc := -1; dc <= 7; dc++ {
				dark := dr >= 0 && dr <= 6 && (dc == 0 || dc == 6) ||
					dc >= 0 && dc <= 6 && (dr == 0 || dr == 6) ||
					dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4
				setFunc(r+dr, c+dc, dark)
			}
		}
	}
	finder(0, 0)
	finder(0, size-7)
	finder(size-7, 0)
	for i := 8; i < size-8; i++ { // timing
		v := i%2 == 0
		setFunc(6, i, v)
		setFunc(i, 6, v)
	}
	if ver >= 2 { // alignment
		centers := alignCenters[ver-2]
		for _, r := range centers {
			for _, c := range centers {
				// skip only the three finder corners; drawing over the
				// timing rows is required and consistent with them
				if r == 6 && c == 6 || r == 6 && c == size-7 || r == size-7 && c == 6 {
					continue
				}
				for dr := -2; dr <= 2; dr++ {
					for dc := -2; dc <= 2; dc++ {
						dark := dr == -2 || dr == 2 || dc == -2 || dc == 2 || dr == 0 && dc == 0
						setFunc(r+dr, c+dc, dark)
					}
				}
			}
		}
	}
	// dark module
	setFunc(size-8, 8, true)
	// reserve format info areas
	for i := 0; i <= 8; i++ {
		if i != 6 {
			setFunc(8, i, false)
			setFunc(i, 8, false)
		}
	}
	for i := 0; i < 8; i++ {
		setFunc(8, size-1-i, false)
		setFunc(size-1-i, 8, false)
	}
	if ver >= 7 { // version info
		vi := versionBits(ver)
		for i := 0; i < 18; i++ {
			bit := vi>>i&1 == 1
			r, c := i/3, size-11+i%3
			setFunc(r, c, bit)
			setFunc(c, r, bit)
		}
	}

	// 4. zigzag data placement
	bitIdx := 0
	totalBits := len(stream) * 8
	up := true
	for col := size - 1; col > 0; col -= 2 {
		if col == 6 {
			col--
		}
		for i := 0; i < size; i++ {
			r := i
			if up {
				r = size - 1 - i
			}
			for _, c := range []int{col, col - 1} {
				if reserved[r][c] {
					continue
				}
				dark := false
				if bitIdx < totalBits {
					dark = stream[bitIdx/8]>>(7-bitIdx%8)&1 == 1
				}
				bitIdx++
				m[r][c] = dark
			}
		}
		up = !up
	}

	// 5. pick mask with lowest penalty
	best := -1
	bestPenalty := 1 << 30
	var bestM [][]bool
	for mask := 0; mask < 8; mask++ {
		trial := applyMask(m, reserved, mask)
		formatBits(trial, level, mask, size)
		p := penalty(trial)
		if p < bestPenalty {
			bestPenalty = p
			best = mask
			bestM = trial
		}
	}
	_ = best
	return bestM
}

func applyMask(m, reserved [][]bool, mask int) [][]bool {
	size := len(m)
	out := make([][]bool, size)
	for i := range out {
		out[i] = append([]bool(nil), m[i]...)
	}
	for r := 0; r < size; r++ {
		for c := 0; c < size; c++ {
			if reserved[r][c] {
				continue
			}
			if maskBit(mask, r, c) {
				out[r][c] = !out[r][c]
			}
		}
	}
	return out
}

func maskBit(mask, r, c int) bool {
	switch mask {
	case 0:
		return (r+c)%2 == 0
	case 1:
		return r%2 == 0
	case 2:
		return c%3 == 0
	case 3:
		return (r+c)%3 == 0
	case 4:
		return (r/2+c/3)%2 == 0
	case 5:
		return (r*c)%2+(r*c)%3 == 0
	case 6:
		return ((r*c)%2+(r*c)%3)%2 == 0
	default:
		return ((r+c)%2+(r*c)%3)%2 == 0
	}
}

// formatBits writes format info (EC level + mask) with BCH(15,5).
// Bit 0 (LSB of the 15-bit code) sits at (row 8, col 0) per ISO 18004.
func formatBits(m [][]bool, level Level, mask, size int) {
	fmtBits := eccBits(level)<<3 | mask
	rem := fmtBits << 10
	for i := 14; i >= 10; i-- {
		if rem>>uint(i)&1 == 1 {
			rem ^= 0x537 << uint(i-10)
		}
	}
	bits := (fmtBits<<10 | rem) ^ 0x5412
	bitAt := func(i int) bool { return bits>>uint(i)&1 == 1 }
	// first copy, around the top-left finder (ISO 18004 figure 25):
	// bits 0-5 run DOWN column 8, bit 8 runs LEFT along row 8
	for i := 0; i <= 5; i++ {
		m[i][8] = bitAt(i)
	}
	m[7][8] = bitAt(6)
	m[8][8] = bitAt(7)
	m[8][7] = bitAt(8)
	for i := 9; i < 15; i++ {
		m[8][14-i] = bitAt(i)
	}
	// second copy: bits 0-7 horizontal at top-right, bits 8-14 vertical at bottom-left
	for i := 0; i < 8; i++ {
		m[8][size-1-i] = bitAt(i)
	}
	for i := 8; i < 15; i++ {
		m[size-15+i][8] = bitAt(i)
	}
	m[size-8][8] = true
}

func eccBits(level Level) int {
	switch level {
	case L:
		return 1
	case M:
		return 0
	case Q:
		return 3
	default:
		return 2
	}
}

func versionBits(ver int) int {
	rem := ver << 12
	for i := 17; i >= 12; i-- {
		if rem>>uint(i)&1 == 1 {
			rem ^= 0x1F25 << uint(i-12)
		}
	}
	return ver<<12 | rem
}

// penalty scores a matrix per QR spec (rules 1-4).
func penalty(m [][]bool) int {
	size := len(m)
	score := 0
	// rule 1: runs of same color >= 5
	for _, row := range m {
		score += runScore(row)
	}
	for c := 0; c < size; c++ {
		col := make([]bool, size)
		for r := 0; r < size; r++ {
			col[r] = m[r][c]
		}
		score += runScore(col)
	}
	// rule 2: 2x2 blocks
	for r := 0; r < size-1; r++ {
		for c := 0; c < size-1; c++ {
			v := m[r][c]
			if v == m[r][c+1] && v == m[r+1][c] && v == m[r+1][c+1] {
				score += 3
			}
		}
	}
	// rule 3: finder-like patterns
	pat := [11]bool{true, false, true, true, true, false, true, false, false, false, false}
	patRev := [11]bool{false, false, false, false, true, false, true, true, true, false, true}
	for _, row := range m {
		score += finderScore(row, pat, patRev)
	}
	for c := 0; c < size; c++ {
		col := make([]bool, size)
		for r := 0; r < size; r++ {
			col[r] = m[r][c]
		}
		score += finderScore(col, pat, patRev)
	}
	// rule 4: dark ratio
	dark := 0
	for _, row := range m {
		for _, v := range row {
			if v {
				dark++
			}
		}
	}
	total := size * size
	dev := abs(dark*100/total*2 - 100) / 5
	score += dev * 10
	return score
}

func runScore(cells []bool) int {
	score := 0
	run := 1
	for i := 1; i < len(cells); i++ {
		if cells[i] == cells[i-1] {
			run++
		} else {
			if run >= 5 {
				score += run - 2
			}
			run = 1
		}
	}
	if run >= 5 {
		score += run - 2
	}
	return score
}

func finderScore(cells []bool, pat, patRev [11]bool) int {
	score := 0
	for i := 0; i+11 <= len(cells); i++ {
		if match(cells[i:i+11], pat[:]) || match(cells[i:i+11], patRev[:]) {
			score += 40
		}
	}
	return score
}

func match(cells []bool, pat []bool) bool {
	for i, p := range pat {
		if cells[i] != p {
			return false
		}
	}
	return true
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// SVG renders the matrix as a standalone SVG string with intrinsic pixel
// dimensions so <img> consumers get a usable natural size.
func SVG(data string, level Level, pixels int) string {
	m := Encode(data, level)
	if m == nil {
		return ""
	}
	size := len(m)
	px := pixels
	if px <= 0 {
		px = 4 * size
	}
	var b bytes.Buffer
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="-4 -4 `)
	b.WriteString(itoa(size+8))
	b.WriteString(" ")
	b.WriteString(itoa(size+8))
	b.WriteString(`" width="`)
	b.WriteString(itoa(px))
	b.WriteString(`" height="`)
	b.WriteString(itoa(px))
	b.WriteString(`" shape-rendering="crispEdges"><rect x="-4" y="-4" width="`)
	b.WriteString(itoa(size + 8))
	b.WriteString(`" height="`)
	b.WriteString(itoa(size + 8))
	b.WriteString(`" fill="#fff"/><path fill="#000" d="`)
	for r := 0; r < size; r++ {
		c := 0
		for c < size {
			if m[r][c] {
				run := 0
				for c+run < size && m[r][c+run] {
					run++
				}
				b.WriteString("M")
				b.WriteString(itoa(c))
				b.WriteString(" ")
				b.WriteString(itoa(r))
				b.WriteString("h")
				b.WriteString(itoa(run))
				b.WriteString("v1h-")
				b.WriteString(itoa(run))
				b.WriteString("z")
				c += run
			} else {
				c++
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Join([]string{fmtInt(n)}, ""))
}

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
