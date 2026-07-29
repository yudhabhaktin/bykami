package frames

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// frameWith draws an opaque sheet and punches the given rectangles clear.
// Antialiasing is imitated by a one-pixel half-alpha border inside each hole,
// which is what a real renderer produces and what Detect has to include.
func frameWith(w, h int, holes []Cell) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{0x20, 0x20, 0x20, 0xff})
		}
	}
	for _, c := range holes {
		for y := c.Y; y < c.Y+c.H; y++ {
			for x := c.X; x < c.X+c.W; x++ {
				edge := x == c.X || y == c.Y || x == c.X+c.W-1 || y == c.Y+c.H-1
				a := uint8(0)
				if edge {
					a = 0x80
				}
				img.SetNRGBA(x, y, color.NRGBA{0x20, 0x20, 0x20, a})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestDetectFindsCellsInReadingOrder(t *testing.T) {
	want := []Cell{
		{X: 30, Y: 36, W: 540, H: 450},
		{X: 30, Y: 516, W: 540, H: 450},
		{X: 30, Y: 996, W: 540, H: 450},
	}
	w, h, got, err := Detect(frameWith(600, 1800, want))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if w != 600 || h != 1800 {
		t.Errorf("sheet = %dx%d, want 600x1800", w, h)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d cells, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cell %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The partially transparent edge belongs to the cell. If Detect thresholded on
// fully-transparent instead, every cell would come back two pixels small and
// the frame's antialiased rim would print over white rather than over the
// photo — a pale halo around all four sides.
func TestDetectIncludesTheAntialiasedRim(t *testing.T) {
	_, _, got, err := Detect(frameWith(600, 1800, []Cell{{X: 100, Y: 100, W: 400, H: 400}}))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if want := (Cell{X: 100, Y: 100, W: 400, H: 400}); got[0] != want {
		t.Errorf("cell = %+v, want %+v", got[0], want)
	}
}

// Two cells side by side, the right one a pixel lower than the left. Sorting on
// y alone would put the whole row below this one in between, and the photos
// would print in the wrong slots.
func TestDetectOrdersRaggedRowsLeftToRight(t *testing.T) {
	_, _, got, err := Detect(frameWith(1200, 1800, []Cell{
		{X: 620, Y: 101, W: 500, H: 700}, // top-right, one pixel low
		{X: 60, Y: 100, W: 500, H: 700},  // top-left
		{X: 60, Y: 900, W: 500, H: 700},  // bottom-left
	}))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	wantX := []int{60, 620, 60}
	wantY := []int{100, 101, 900}
	for i := range wantX {
		if got[i].X != wantX[i] || got[i].Y != wantY[i] {
			t.Errorf("cell %d = (%d,%d), want (%d,%d)", i, got[i].X, got[i].Y, wantX[i], wantY[i])
		}
	}
}

// A decorative cut-out is not a photo slot. Without the size and shape checks
// this frame would ask the customer for a fourth photo and print it inside a
// star.
func TestDetectIgnoresDecorativeHoles(t *testing.T) {
	art := frameWith(600, 1800, []Cell{
		{X: 30, Y: 36, W: 540, H: 450},
		{X: 30, Y: 516, W: 540, H: 450},
		{X: 30, Y: 996, W: 540, H: 450},
	})
	img, err := png.Decode(bytes.NewReader(art))
	if err != nil {
		t.Fatal(err)
	}
	nrgba := img.(*image.NRGBA)
	// A transparent diamond in the footer: big enough to pass a size check on
	// its own, nowhere near rectangular.
	for dy := -60; dy <= 60; dy++ {
		for dx := -60; dx <= 60; dx++ {
			if abs(dx)+abs(dy) <= 60 {
				nrgba.SetNRGBA(300+dx, 1600+dy, color.NRGBA{})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, nrgba); err != nil {
		t.Fatal(err)
	}

	_, _, got, err := Detect(buf.Bytes())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d cells, want 3 — the diamond was counted: %+v", len(got), got)
	}
}

func TestDetectRejectsArtworkWithNoHoles(t *testing.T) {
	_, _, _, err := Detect(frameWith(600, 1800, nil))
	if !errors.Is(err, ErrOpaque) {
		t.Fatalf("err = %v, want ErrOpaque", err)
	}
}

func TestDetectRejectsNonPNG(t *testing.T) {
	_, _, _, err := Detect([]byte("this is not a png"))
	if !errors.Is(err, ErrNotPNG) {
		t.Fatalf("err = %v, want ErrNotPNG", err)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
