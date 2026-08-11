package qr

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
)

// The check that matters, and the reason to believe any of this.
//
// A wrong QR encoder does not fail: it produces a square that looks entirely
// convincing and that no phone will read, and the way anyone finds out is an
// operator standing at a terminal with the camera open. So these three symbols
// come from somewhere else — the `qrcode` npm package in this repository's
// lockfile, which the kiosk already draws its payment codes with — captured
// with segmentation forced to byte mode to match what this package does.
//
// A failure here is this package having drifted, not the expectation being
// stale. The generator is in the commit message; regenerating it to make a
// failure go away would throw away the only independent opinion in the file.
func TestMatchesTheReferenceEncoder(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		version int
		mask    int
		want    []string
	}{
		{
			name:    "short",
			text:    "hello",
			version: 1,
			mask:    0,
			want: []string{
				"#######..##...#######",
				"#.....#.##....#.....#",
				"#.###.#..#.##.#.###.#",
				"#.###.#...##..#.###.#",
				"#.###.#.##..#.#.###.#",
				"#.....#.....#.#.....#",
				"#######.#.#.#.#######",
				"..........###........",
				"#.#.#.#..#.#....#..#.",
				"..#.##....#...#....##",
				".#.#..#.###.#...#####",
				"##..#.........#....#.",
				".##.#.##..#.#.#.#....",
				"........####.#.#..###",
				"#######...##.###..###",
				"#.....#...####.##....",
				"#.###.#.#.##.###...##",
				"#.###.#..#....##..##.",
				"#.###.#.###.#...#.#.#",
				"#.....#..#....#.#..#.",
				"#######.###.#.##...##",
			},
		},
		{
			name:    "an otpauth URI",
			text:    "otpauth://totp/bykami:%2B6281234567890?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=bykami",
			version: 6,
			mask:    4,
			want: []string{
				"#######.##.######..#.##..#.#.###..#######",
				"#.....#..###.##...#####.###.#####.#.....#",
				"#.###.#...#.#.#####...##..#####...#.###.#",
				"#.###.#.#.##.##.##.###..#...#.#.#.#.###.#",
				"#.###.#.#.#.####...#...###.####.#.#.###.#",
				"#.....#.#.##.#..####....###.##.#..#.....#",
				"#######.#.#.#.#.#.#.#.#.#.#.#.#.#.#######",
				"........#..#..#..#.###.##...#####........",
				"#...#.###.##.##.#..#.##.#.#.##########..#",
				"#.###..#.##..###.##.##.#.######.###.#..##",
				".##..###.###...###..##.##.####...##.###.#",
				".####...##.###...##.##....##.#.#.#.###...",
				".#..####.###.##..####.##.....##.#..#.....",
				"#.###..####.##.##..#....###.##...##.##...",
				"##.#.####.#..##..#.#.#####..#...#.#.#...#",
				"####...#.#.#####.#.#####..#..#....##...##",
				".#...#####.##...###...#.##.##....#.#.....",
				".#..#..#.##..#######......#.#.##.####.###",
				"#...#.#.#####.#...#..#######.#....#..#.##",
				"#.#.##..#..##.###....#.###...##..###.#.#.",
				"..#..####..#.#.##..#.....#..##.....#.##..",
				".#.....#...#.#....##.###..#####.#######.#",
				"#....###..#..####...##.#####..#..##..##.#",
				".#.#...#.#####..##.#.#.##.#..####....#.##",
				"#..#.##..##..#.#.######.#..###.#.#..#..##",
				".#..##.#.##..#.##...##.#.#####..##.##.###",
				"...#..##..####........####.#.#.....#....#",
				"###.....############.###..#..##...##.#..#",
				"##.#..#....#.#.#......###..#.##..#.#....#",
				"#.#.#....##...#.#.####...#........#.####.",
				"..##.##..#####..#.#.#.#...##......####..#",
				"....##.#.##...#.##...#..#..###.#.###...##",
				"####.##.##.#.#.###.#.#.###.##..######..#.",
				"........######.##.##.##..#..##..#...##.##",
				"#######.##.##...##..#..#...#.##.#.#.#..##",
				"#.....#..#.#.#######..##..#.#####...##.#.",
				"#.###.#.##.##..##.##..#.###.##.######.##.",
				"#.###.#......#.....#.#.#.#.#..####....##.",
				"#.###.#..##...####...###.#.####..#...#..#",
				"#.....#..#.#.#...#.#.#.....###.##.##.#.##",
				"#######.##.#....#..#.#.......#.###..##.#.",
			},
		},
		{
			name:    "an otpauth URI with every parameter spelled out",
			text:    "otpauth://totp/studio%20by%20KAMI:%2B6281234567890?secret=MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U&issuer=studio%20by%20KAMI&algorithm=SHA1&digits=6&period=30",
			version: 8,
			mask:    2,
			want: []string{
				"#######..#.##..#.###...###.#.###.#.###..#.#######",
				"#.....#..#.##..#.#.#.#.#..##.#..#.#.#####.#.....#",
				"#.###.#.#..#.#..#####......#######.....##.#.###.#",
				"#.###.#.#.#...##...#..###.#..##...####.#..#.###.#",
				"#.###.#.##..###..#...######...#.#....#....#.###.#",
				"#.....#.#....#....##..#...####....#####...#.....#",
				"#######.#.#.#.#.#.#.#.#.#.#.#.#.#.#.#.#.#.#######",
				"........##.#.###.#...##...##.##..#....#.#........",
				"#.#####..#..#.....#...#####....#..###..##.#####..",
				".###.#..#...##..#.##.#.#...#.###...##....####..#.",
				"##...##.#.#.##.#.#.#####.##..####.######...#..#.#",
				".#..##.#.#.#........#....#..##..####..#..###.....",
				"#.############........###.##.##....##.....##.#..#",
				".....#.#.##..#.....##...##...####..#.#...##..###.",
				"...#..#....##.####..##..#....#.##.##.......#...##",
				".#.##..#######.#...###.####.###.##..#.########.#.",
				".##..###.##.##.###....#####...##...##...#.#..###.",
				"...#.#.#.#..##.#...#.##.#..#######..##.#..###...#",
				"#####.###.#####..#.....##.#..#.#.##...#.##...#.##",
				"#.#.#..#..#.#........#..##.##.###....#....###..##",
				"#..#..##.....#.##.#...####.....#.#..#..##.##..##.",
				"##.##...##.#..###..#.#.###.#..##...###.#.###.####",
				"..#########....##..########..#.#..###.#.######.##",
				".####...#...#...##..#.#...##..#..#....#.#...##...",
				"..###.#.####.###..#..##.#.#....#.#####..#.#.#.##.",
				".#.##...####..####.#.##...######...#....#...#..#.",
				"#..#######.#.#..#...#.#######.#####.###.#####.#.#",
				".##....##..#.##.##....#.#####.#.##...#..#.##.#...",
				"..#..##.#...##.......#.....#.#.#..#.##.###..#.###",
				"..........#...####...####....###....#..#.#.#.....",
				".##.#.#.....#...#......###.###..#.#.###.###..####",
				".##..#.###..####.#.........##.#.#...######.##....",
				".#...##...#.#.###..#####.###.#.#.#####.#...#.####",
				"#.####...#........#..##.##.##.##...#......##.#..#",
				"...##.####..#..###..#....#...#..###.#####.####.##",
				"..#.##...#...###..#...#.##.##.#.#..####..#.....##",
				".###..#..#.#..#.###....###....##...#####.######.#",
				"##.#....#.#.######.#.#.###...###...#.#..###..#..#",
				".#...##...#.#.###.#............#.##.####..#.#..##",
				".###....####.####..###...##..####..#.....#...#...",
				"###...#######..#.##########..#.....##..######.##.",
				"........####.##..##...#...#.###....#...##...##.#.",
				"#######..#.#..#.#..#.##.#.#...##..##.##.#.#.###.#",
				"#.....#.##...##...##.##...#.###.##.#.#.##...##...",
				"#.###.#.##.#.#..##.##.#####...##..###...#####.#.#",
				"#.###.#.#.###.####...#..#..####.....#..###.###.##",
				"#.###.#.###.###.#.###..##.##.#.#..#####..#.#.....",
				"#.....#..###..##.#.....#.#..##..#..#########....#",
				"#######.#.....#..#.###....##.###.#.###.#..#..####",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, err := Encode(tc.text)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if code.Version != tc.version {
				t.Errorf("version = %d, want %d", code.Version, tc.version)
			}
			// Not decoration: the mask is chosen by scoring all eight, so a
			// disagreement here means the penalty rules have drifted even if
			// every module still happens to line up.
			if code.Mask != tc.mask {
				t.Errorf("mask = %d, want %d", code.Mask, tc.mask)
			}
			if code.Size != len(tc.want) {
				t.Fatalf("size = %d, want %d", code.Size, len(tc.want))
			}

			for row := range code.Size {
				var got strings.Builder
				for col := range code.Size {
					if code.Dark(row, col) {
						got.WriteByte('#')
					} else {
						got.WriteByte('.')
					}
				}
				if got.String() != tc.want[row] {
					t.Errorf("row %d\n got %s\nwant %s", row, got.String(), tc.want[row])
				}
			}
		})
	}
}

// The version has to grow with the payload, and the boundary is where an
// off-by-one in the capacity arithmetic would show up: one byte too many
// silently overflowing into a symbol that cannot hold it.
func TestTheSymbolGrowsWithTheInput(t *testing.T) {
	tests := []struct {
		bytes   int
		version int
	}{
		{14, 1}, // exactly what version 1 holds at level M
		{15, 2}, // one more, so the next version up
		{26, 2}, // and the same boundary again
		{27, 3},
		{106, 6},
		{107, 7}, // the first version carrying a version-information block
		{213, 10},
	}

	for _, tc := range tests {
		code, err := Encode(strings.Repeat("x", tc.bytes))
		if err != nil {
			t.Fatalf("%d bytes: %v", tc.bytes, err)
		}
		if code.Version != tc.version {
			t.Errorf("%d bytes = version %d, want %d", tc.bytes, code.Version, tc.version)
		}
		if want := code.Version*4 + 17; code.Size != want {
			t.Errorf("version %d is %d modules, want %d", code.Version, code.Size, want)
		}
	}
}

// Refused rather than truncated. A symbol silently missing its last few bytes
// would scan perfectly and enrol the wrong secret.
func TestTooMuchDataIsAnError(t *testing.T) {
	if _, err := Encode(strings.Repeat("x", 214)); err == nil {
		t.Fatal("214 bytes was accepted; version 10 holds 213")
	}
}

// The quiet zone is not decoration: without it a scanner cannot find the
// symbol's edge, which is the usual reason a QR printed flush against text
// will not read.
func TestTerminalLeavesAQuietZone(t *testing.T) {
	code, err := Encode("hello")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(code.Terminal(), "\n"), "\n")
	// Two module rows per line of text, rounded up.
	if want := (code.Size + 2*quietZone + 1) / 2; len(lines) != want {
		t.Fatalf("%d lines, want %d", len(lines), want)
	}

	for _, at := range []int{0, 1, len(lines) - 1} {
		body := strings.TrimSuffix(strings.TrimPrefix(lines[at], "\x1b[30;107m"), "\x1b[0m")
		if strings.TrimSpace(body) != "" {
			t.Errorf("line %d is inside the quiet zone but has ink: %q", at, body)
		}
	}

	// Colours set explicitly, because a terminal on a dark theme would
	// otherwise render the symbol inverted and many scanners see nothing.
	if !strings.HasPrefix(lines[0], "\x1b[") {
		t.Error("terminal output does not set its own colours")
	}
}

func TestPNGCarriesTheSymbol(t *testing.T) {
	code, err := Encode("hello")
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	const scale = 4
	raw, err := code.PNG(scale)
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	side := (code.Size + 2*quietZone) * scale
	if b := img.Bounds(); b.Dx() != side || b.Dy() != side {
		t.Fatalf("image is %dx%d, want %dx%d", b.Dx(), b.Dy(), side, side)
	}

	isDark := func(x, y int) bool {
		r, g, b, _ := img.At(x, y).RGBA()
		return r == 0 && g == 0 && b == 0
	}
	if isDark(0, 0) {
		t.Error("the top-left corner is dark, so the quiet zone is missing")
	}
	// The top-left module of the top-left finder, which is dark in every symbol.
	if !isDark(quietZone*scale, quietZone*scale) {
		t.Error("the finder pattern is not where the quiet zone says it should be")
	}
}
