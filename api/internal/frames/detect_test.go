package frames

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
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

// punch clears every pixel for which in(x, y) reports true.
//
// Redrawn into an NRGBA rather than type-asserted: artwork with no holes yet
// encodes without an alpha channel and decodes back as an *image.RGBA.
func punch(art []byte, in func(x, y int) bool) []byte {
	src, err := png.Decode(bytes.NewReader(art))
	if err != nil {
		panic(err)
	}
	b := src.Bounds()
	nrgba := image.NewNRGBA(b)
	draw.Draw(nrgba, b, src, b.Min, draw.Src)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if in(x, y) {
				nrgba.SetNRGBA(x, y, color.NRGBA{})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, nrgba); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// A slot does not have to be square. Round and heart-shaped holes are ordinary
// frame design: the photo fills the bounding box and the artwork drawn over it
// masks the photo back to the cut shape, so nothing prints outside the hole.
//
// A circle scores 0.79 and a heart about 0.70. Rejecting those loses a whole
// row of a real frame silently — the customer is never asked for those photos
// and the holes print blank.
func TestDetectAcceptsRoundSlots(t *testing.T) {
	const cx, cy, r = 300, 900, 200
	art := punch(frameWith(600, 1800, nil), func(x, y int) bool {
		dx, dy := x-cx, y-cy
		return dx*dx+dy*dy <= r*r
	})

	_, _, got, err := Detect(art)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d cells, want 1 — the round slot was dropped: %+v", len(got), got)
	}
	// The bounding box of the circle, which is where the photo goes.
	if want := (Cell{X: cx - r, Y: cy - r, W: 2*r + 1, H: 2*r + 1}); got[0] != want {
		t.Errorf("cell = %+v, want %+v", got[0], want)
	}
}

// The shape check earns its keep here rather than on decoration, which is small
// enough for the size check to catch. A design whose middle is transparent and
// whose border is the artwork is one enormous hole — accepting it would offer a
// cell the size of the sheet and print a face over the whole frame.
func TestDetectRejectsATransparentMiddle(t *testing.T) {
	outer := image.Rect(40, 40, 560, 1760)
	inner := image.Rect(90, 90, 510, 1710)
	art := punch(frameWith(600, 1800, nil), func(x, y int) bool {
		p := image.Pt(x, y)
		return p.In(outer) && !p.In(inner)
	})

	_, _, _, err := Detect(art)
	if !errors.Is(err, ErrNoCells) {
		t.Fatalf("err = %v, want ErrNoCells — the border ring was read as a slot", err)
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
