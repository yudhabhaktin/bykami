// Package qr encodes a QR symbol and renders it to a terminal or a PNG.
//
// It exists because enrolling an authenticator is a shell job — see
// cmd/bykami/admin.go — and the thing being enrolled is a QR code. The obvious
// alternative was a dependency, and there is a good one; what rules it out is
// that CI checks `go mod tidy` leaves go.mod and go.sum unchanged, and the
// machine this was written on has no Go toolchain to run it with. A module that
// cannot be tidied is a build that fails on the first push.
//
// Deliberately narrow: byte mode only, error-correction level M, versions 1
// through 10. That covers any otpauth:// URI with room to spare and nothing
// else, which is the whole requirement. Byte mode rather than the usual
// automatic segmentation into alphanumeric runs — an otpauth URI would encode a
// version or two smaller if its uppercase secret were split into its own
// segment — because segmentation is a optimiser to get wrong for a symbol that
// is already small enough.
//
// # On trusting this
//
// A QR encoder is the kind of code that produces a plausible-looking square
// while being subtly wrong, and the failure is discovered by a person holding a
// phone. So it was written twice: first as a prototype checked against the
// `qrcode` npm package already in this repository's lockfile, over every input
// length from 1 to 213 bytes, matching version, mask and every module; then
// ported here. TestMatchesTheReferenceEncoder holds three of those symbols.
//
// That is also why the penalty scoring below follows that library rather than
// the stricter reading of ISO/IEC 18004 — matching an implementation that is
// known to be scanned by real phones is worth more here than matching the
// wording of a specification, and mask choice affects only how pleasant a
// symbol is to read, never whether it decodes.
package qr

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
)

const (
	minVersion = 1
	maxVersion = 10

	// The quiet zone the specification requires: four light modules on every
	// side. Scanners use it to find the symbol's edge, and a QR pasted flush
	// against surrounding text is the usual reason one will not read.
	quietZone = 4
)

// blocksM is the block structure for error-correction level M, indexed by
// version: error-correction codewords per block, then the count and data-codeword
// size of the first group of blocks and of the second, which most versions do
// not have.
var blocksM = [maxVersion + 1][5]int{
	1:  {10, 1, 16, 0, 0},
	2:  {16, 1, 28, 0, 0},
	3:  {26, 1, 44, 0, 0},
	4:  {18, 2, 32, 0, 0},
	5:  {24, 2, 43, 0, 0},
	6:  {16, 4, 27, 0, 0},
	7:  {18, 4, 31, 0, 0},
	8:  {22, 2, 38, 2, 39},
	9:  {22, 3, 36, 2, 37},
	10: {26, 4, 43, 1, 44},
}

// alignmentCentres are the row and column coordinates whose every combination
// carries an alignment pattern, minus the three that would land on a finder.
var alignmentCentres = [maxVersion + 1][]int{
	2:  {6, 18},
	3:  {6, 22},
	4:  {6, 26},
	5:  {6, 30},
	6:  {6, 34},
	7:  {6, 22, 38},
	8:  {6, 24, 42},
	9:  {6, 26, 46},
	10: {6, 28, 50},
}

// remainderBits pad the symbol out to its module count when the codewords do
// not fill it exactly. They carry nothing and are left light.
var remainderBits = [maxVersion + 1]int{2: 7, 3: 7, 4: 7, 5: 7, 6: 7}

// Code is one encoded symbol.
type Code struct {
	Version int
	Size    int // modules per side, excluding the quiet zone
	Mask    int

	modules []bool // row-major, Size*Size
}

// Encode returns the smallest symbol that holds text.
func Encode(text string) (*Code, error) {
	data := []byte(text)
	version, err := pickVersion(len(data))
	if err != nil {
		return nil, err
	}
	codewords := interleave(encodeData(data, version), version)

	// Every mask is tried and the least ugly kept, which is what the
	// specification asks for. Eight encodes of a symbol this size is
	// microseconds, and it happens once per enrolment.
	var best *matrix
	bestMask, bestScore := 0, 0
	for mask := range 8 {
		m := newMatrix(version)
		m.placeData(codewords, version)
		m.drawVersion(version)
		m.applyMask(mask)
		m.drawFormat(mask)

		if score := m.penalty(); best == nil || score < bestScore {
			best, bestMask, bestScore = m, mask, score
		}
	}

	return &Code{Version: version, Size: best.size, Mask: bestMask, modules: best.modules}, nil
}

// Dark reports whether one module is dark. Anything outside the symbol is
// light, so callers can walk the quiet zone without a bounds check of their own.
func (c *Code) Dark(row, col int) bool {
	if row < 0 || row >= c.Size || col < 0 || col >= c.Size {
		return false
	}
	return c.modules[row*c.Size+col]
}

// ---------------------------------------------------------------- rendering

// Terminal renders the symbol as text, two module rows per line.
//
// Two rows per line because a character cell is about twice as tall as it is
// wide, so one row per line prints a symbol stretched to double height — which
// scanners read poorly and which does not fit on a screen at these versions.
// The half-block characters give square modules instead.
//
// The colours are set explicitly rather than left to the terminal, and that is
// the part worth not undoing. A QR is read dark-on-light; on the dark theme
// most terminals ship with, an uncoloured symbol comes out inverted, and while
// some scanners cope with that, plenty simply see nothing.
func (c *Code) Terminal() string {
	const (
		reset = "\x1b[0m"
		ink   = "\x1b[30;107m" // black on bright white
	)

	side := c.Size + 2*quietZone
	var b strings.Builder
	for row := 0; row < side; row += 2 {
		b.WriteString(ink)
		for col := range side {
			top := c.Dark(row-quietZone, col-quietZone)
			bottom := c.Dark(row+1-quietZone, col-quietZone)
			switch {
			case top && bottom:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bottom:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
		}
		b.WriteString(reset)
		b.WriteByte('\n')
	}
	return b.String()
}

// PNG renders the symbol at scale pixels per module, for when the terminal
// cannot be trusted to draw block characters — a Windows console on a legacy
// code page being the case that prompted it.
func (c *Code) PNG(scale int) ([]byte, error) {
	if scale < 1 {
		scale = 1
	}
	side := (c.Size + 2*quietZone) * scale

	// Two colours, so a paletted image rather than RGBA: a 400-pixel symbol is
	// a couple of kilobytes this way.
	img := image.NewPaletted(image.Rect(0, 0, side, side), color.Palette{color.White, color.Black})
	for y := range side {
		for x := range side {
			if c.Dark(y/scale-quietZone, x/scale-quietZone) {
				img.SetColorIndex(x, y, 1)
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("qr: encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------- data

func dataCodewords(version int) int {
	b := blocksM[version]
	return b[1]*b[2] + b[3]*b[4]
}

func pickVersion(n int) (int, error) {
	for v := minVersion; v <= maxVersion; v++ {
		if 4+lengthBits(v)+8*n <= dataCodewords(v)*8 {
			return v, nil
		}
	}
	return 0, fmt.Errorf("qr: %d bytes does not fit a version %d symbol", n, maxVersion)
}

// lengthBits is the width of the character-count field, which widens at
// version 10.
func lengthBits(version int) int {
	if version >= 10 {
		return 16
	}
	return 8
}

// bitWriter packs bits into codewords, most significant first.
type bitWriter struct {
	out  []byte
	used int // bits written into the last byte, 0 when it is full
}

func (w *bitWriter) write(value, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.used == 0 {
			w.out = append(w.out, 0)
		}
		w.out[len(w.out)-1] |= byte((value>>i)&1) << (7 - w.used)
		w.used = (w.used + 1) % 8
	}
}

func (w *bitWriter) bitLen() int {
	if w.used == 0 {
		return len(w.out) * 8
	}
	return (len(w.out)-1)*8 + w.used
}

// encodeData turns the payload into the version's full complement of data
// codewords: mode, length, the bytes, a terminator, and padding.
func encodeData(data []byte, version int) []byte {
	var w bitWriter
	w.write(0b0100, 4) // byte mode
	w.write(len(data), lengthBits(version))
	for _, b := range data {
		w.write(int(b), 8)
	}

	// The terminator is four zero bits, or fewer when the symbol is nearly
	// full — writing all four regardless would overrun the last codeword.
	capacity := dataCodewords(version) * 8
	w.write(0, min(4, capacity-w.bitLen()))
	if w.used != 0 {
		w.write(0, 8-w.used)
	}

	// Then the two pad codewords, alternating, until the data area is full.
	out := w.out
	for i := 0; len(out) < dataCodewords(version); i++ {
		if i%2 == 0 {
			out = append(out, 0xEC)
		} else {
			out = append(out, 0x11)
		}
	}
	return out
}

// interleave splits the data into blocks, computes each block's error
// correction, and reads the lot back out in the order the symbol wants: one
// codeword from each block in turn, data first and then correction.
//
// The interleaving is what makes the error correction worth having. A scratch
// across the symbol lands in one region of the picture but is spread over every
// block, so no single block takes damage past what it can repair.
func interleave(data []byte, version int) []byte {
	b := blocksM[version]
	ecPerBlock, group1, size1, group2, size2 := b[0], b[1], b[2], b[3], b[4]

	blocks := make([][]byte, 0, group1+group2)
	for at := 0; len(blocks) < group1; at += size1 {
		blocks = append(blocks, data[at:at+size1])
	}
	for at := group1 * size1; len(blocks) < group1+group2; at += size2 {
		blocks = append(blocks, data[at:at+size2])
	}

	correction := make([][]byte, len(blocks))
	for i, block := range blocks {
		correction[i] = errorCorrection(block, ecPerBlock)
	}

	out := make([]byte, 0, len(data)+ecPerBlock*len(blocks))
	for i := range max(size1, size2) {
		for _, block := range blocks {
			// The shorter blocks of group one run out first; the specification
			// simply skips them for the last round.
			if i < len(block) {
				out = append(out, block[i])
			}
		}
	}
	for i := range ecPerBlock {
		for _, ec := range correction {
			out = append(out, ec[i])
		}
	}
	return out
}

// ---------------------------------------------------------------- GF(256)

// Reed-Solomon over the field the QR specification names, x^8 + x^4 + x^3 +
// x^2 + 1. The tables turn multiplication into an addition of logarithms; the
// exponent table is stored twice end to end so that the addition never has to
// be reduced modulo 255.
var (
	gfExp [512]byte
	gfLog [256]byte
)

func init() {
	x := 1
	for i := range 255 {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
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

// generator returns the polynomial whose roots are the first n powers of the
// field's primitive element, highest degree first.
func generator(n int) []byte {
	p := []byte{1}
	for i := range n {
		next := make([]byte, len(p)+1)
		for j, c := range p {
			next[j] ^= c                    // multiplied by x
			next[j+1] ^= gfMul(c, gfExp[i]) // multiplied by the root
		}
		p = next
	}
	return p
}

// errorCorrection returns the n correction codewords for one block: the
// remainder of the block divided by the generator polynomial.
func errorCorrection(block []byte, n int) []byte {
	g := generator(n)
	work := make([]byte, len(block)+n)
	copy(work, block)

	for i := range block {
		factor := work[i]
		if factor == 0 {
			continue
		}
		for j, c := range g {
			work[i+j] ^= gfMul(c, factor)
		}
	}
	return work[len(block):]
}

// ---------------------------------------------------------------- matrix

type matrix struct {
	size    int
	modules []bool
	// fixed marks the function patterns — finders, timing, alignment, format
	// and version. Data skips them and the mask must not touch them.
	fixed []bool
}

func newMatrix(version int) *matrix {
	size := version*4 + 17
	m := &matrix{size: size, modules: make([]bool, size*size), fixed: make([]bool, size*size)}

	// The three finder patterns, drawn out to Chebyshev distance four so that
	// the separator around each comes out of the same loop.
	for _, centre := range [3][2]int{{3, 3}, {3, size - 4}, {size - 4, 3}} {
		for dr := -4; dr <= 4; dr++ {
			for dc := -4; dc <= 4; dc++ {
				row, col := centre[0]+dr, centre[1]+dc
				if row < 0 || row >= size || col < 0 || col >= size {
					continue
				}
				d := chebyshev(dr, dc)
				m.setFunction(row, col, d != 2 && d != 4)
			}
		}
	}

	// Timing patterns, which give a scanner the module pitch.
	for i := range size {
		if !m.isFixed(6, i) {
			m.setFunction(6, i, i%2 == 0)
		}
		if !m.isFixed(i, 6) {
			m.setFunction(i, 6, i%2 == 0)
		}
	}

	// Alignment patterns, minus the three corners already carrying a finder.
	if centres := alignmentCentres[version]; len(centres) > 0 {
		last := centres[len(centres)-1]
		for _, row := range centres {
			for _, col := range centres {
				if (row == 6 && col == 6) || (row == 6 && col == last) || (row == last && col == 6) {
					continue
				}
				for dr := -2; dr <= 2; dr++ {
					for dc := -2; dc <= 2; dc++ {
						m.setFunction(row+dr, col+dc, chebyshev(dr, dc) != 1)
					}
				}
			}
		}
	}

	// Reserve what the format and version information will occupy, so that data
	// placement steps over it. The values are written after masking.
	for i := range 9 {
		if !m.isFixed(8, i) {
			m.setFunction(8, i, false)
		}
		if !m.isFixed(i, 8) {
			m.setFunction(i, 8, false)
		}
	}
	for i := range 8 {
		m.setFunction(8, size-1-i, false)
		m.setFunction(size-1-i, 8, false)
	}
	if version >= 7 {
		for i := range 18 {
			a, b := size-11+i%3, i/3
			m.setFunction(b, a, false)
			m.setFunction(a, b, false)
		}
	}
	m.setFunction(size-8, 8, true) // the one module that is always dark

	return m
}

func (m *matrix) at(row, col int) bool      { return m.modules[row*m.size+col] }
func (m *matrix) isFixed(row, col int) bool { return m.fixed[row*m.size+col] }

func (m *matrix) setFunction(row, col int, dark bool) {
	m.modules[row*m.size+col] = dark
	m.fixed[row*m.size+col] = true
}

// placeData walks the symbol in the prescribed order — upwards and downwards
// through pairs of columns, right to left, skipping the vertical timing column
// — laying one bit in each module that is not a function pattern.
func (m *matrix) placeData(codewords []byte, version int) {
	total := len(codewords)*8 + remainderBits[version]
	bit := 0

	for right := m.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5 // column six is the timing pattern; the pair shifts left
		}
		for vertical := range m.size {
			for j := range 2 {
				col := right - j
				row := vertical
				if (right+1)&2 == 0 { // every other column pair runs upwards
					row = m.size - 1 - vertical
				}
				if m.isFixed(row, col) || bit >= total {
					continue
				}
				// The remainder bits past the last codeword stay light.
				if at := bit >> 3; at < len(codewords) {
					m.modules[row*m.size+col] = codewords[at]>>(7-(bit&7))&1 == 1
				}
				bit++
			}
		}
	}
}

func (m *matrix) applyMask(mask int) {
	for row := range m.size {
		for col := range m.size {
			if !m.isFixed(row, col) && masked(mask, row, col) {
				m.modules[row*m.size+col] = !m.modules[row*m.size+col]
			}
		}
	}
}

// masked reports whether one module is inverted by the given mask. The eight
// patterns are fixed by the specification; they exist so that every symbol has
// a version of itself without large blank regions or accidental finder lookalikes.
func masked(mask, row, col int) bool {
	switch mask {
	case 0:
		return (row+col)%2 == 0
	case 1:
		return row%2 == 0
	case 2:
		return col%3 == 0
	case 3:
		return (row+col)%3 == 0
	case 4:
		return (row/2+col/3)%2 == 0
	case 5:
		return (row*col)%2+(row*col)%3 == 0
	case 6:
		return ((row*col)%2+(row*col)%3)%2 == 0
	case 7:
		return ((row*col)%3+(row+col)%2)%2 == 0
	}
	return false
}

// drawFormat writes the error-correction level and mask, protected by a BCH
// code and then XORed with a fixed pattern so that the field is never all
// light. It goes in twice, by each of two finders, so that damage to one corner
// does not cost the reader the only copy.
func (m *matrix) drawFormat(mask int) {
	const levelM = 0b00

	data := levelM<<3 | mask
	remainder := data
	for range 10 {
		remainder = (remainder << 1) ^ ((remainder >> 9) * 0x537)
	}
	bits := (data<<10 | remainder) ^ 0x5412
	bit := func(i int) bool { return bits>>i&1 == 1 }

	for i := range 6 {
		m.setFunction(i, 8, bit(i))
	}
	m.setFunction(7, 8, bit(6))
	m.setFunction(8, 8, bit(7))
	m.setFunction(8, 7, bit(8))
	for i := 9; i < 15; i++ {
		m.setFunction(8, 14-i, bit(i))
	}

	for i := range 8 {
		m.setFunction(8, m.size-1-i, bit(i))
	}
	for i := 8; i < 15; i++ {
		m.setFunction(m.size-15+i, 8, bit(i))
	}
	m.setFunction(m.size-8, 8, true)
}

// drawVersion writes the version number, BCH-protected, into the two blocks
// that versions seven and up carry. Smaller symbols have no such field: a
// scanner counts their modules instead.
func (m *matrix) drawVersion(version int) {
	if version < 7 {
		return
	}

	remainder := version
	for range 12 {
		remainder = (remainder << 1) ^ ((remainder >> 11) * 0x1F25)
	}
	bits := version<<12 | remainder

	for i := range 18 {
		dark := bits>>i&1 == 1
		a, b := m.size-11+i%3, i/3
		m.setFunction(b, a, dark)
		m.setFunction(a, b, dark)
	}
}

// ---------------------------------------------------------------- penalty

// penalty scores a masked symbol on the four features the specification says
// make one hard to read. Lowest wins.
//
// This follows the `qrcode` npm package rather than the letter of the standard,
// for the reason given at the top of the file: it is the implementation this
// was checked against, and the two disagree only about which of eight equally
// valid symbols is prettiest.
func (m *matrix) penalty() int {
	score := 0
	dark := func(row, col int) int {
		if m.at(row, col) {
			return 1
		}
		return 0
	}

	// One: runs of five or more modules of one colour, along every row and
	// every column. Long runs are where a reader loses count.
	for a := range m.size {
		runRow, runCol := 0, 0
		lastRow, lastCol := -1, -1
		for b := range m.size {
			if v := dark(a, b); v == lastRow {
				runRow++
			} else {
				if runRow >= 5 {
					score += 3 + (runRow - 5)
				}
				lastRow, runRow = v, 1
			}
			if v := dark(b, a); v == lastCol {
				runCol++
			} else {
				if runCol >= 5 {
					score += 3 + (runCol - 5)
				}
				lastCol, runCol = v, 1
			}
		}
		if runRow >= 5 {
			score += 3 + (runRow - 5)
		}
		if runCol >= 5 {
			score += 3 + (runCol - 5)
		}
	}

	// Two: blocks of one colour, counted as the two-by-two squares they contain.
	for row := range m.size - 1 {
		for col := range m.size - 1 {
			sum := dark(row, col) + dark(row, col+1) + dark(row+1, col) + dark(row+1, col+1)
			if sum == 0 || sum == 4 {
				score += 3
			}
		}
	}

	// Three: anything that looks like a finder — the 1:1:3:1:1 run with four
	// light modules to one side — matched as an eleven-module sliding window.
	// These are the expensive mistake, because a reader that mistakes one for a
	// corner has the symbol's geometry wrong.
	for a := range m.size {
		windowRow, windowCol := 0, 0
		for b := range m.size {
			windowRow = (windowRow<<1)&0x7FF | dark(a, b)
			windowCol = (windowCol<<1)&0x7FF | dark(b, a)
			if b >= 10 {
				if windowRow == 0x5D0 || windowRow == 0x05D {
					score += 40
				}
				if windowCol == 0x5D0 || windowCol == 0x05D {
					score += 40
				}
			}
		}
	}

	// Four: how far the proportion of dark modules strays from half, in steps
	// of five per cent.
	//
	// Written as one exact division rather than a percentage then a fifth of
	// it: ceil(dark*100/total/5) is ceil(dark*20/total), and doing it in two
	// steps of integer division truncates in between and lands a step out.
	count := 0
	for row := range m.size {
		for col := range m.size {
			count += dark(row, col)
		}
	}
	total := m.size * m.size
	score += abs(ceilDiv(count*20, total)-10) * 10

	return score
}

func chebyshev(a, b int) int { return max(abs(a), abs(b)) }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }
