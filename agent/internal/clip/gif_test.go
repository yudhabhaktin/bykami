package clip_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/bhaktiyudha/bykami/agent/internal/clip"
)

// frames writes n JPEGs that differ from each other, so a test asserting on
// frame count cannot be satisfied by an encoder that collapsed them.
func frames(t *testing.T, dir string, n, w, h int) []string {
	t.Helper()

	var out []string
	for i := range n {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				img.Set(x, y, color.RGBA{R: uint8((x + i*20) % 256), G: uint8(y % 256), B: 90, A: 255})
			}
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
			t.Fatalf("encode frame %d: %v", i, err)
		}

		path := filepath.Join(dir, clip.FrameName(i))
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
		out = append(out, path)
	}
	return out
}

func decode(t *testing.T, path string) *gif.GIF {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open gif: %v", err)
	}
	defer f.Close()

	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatalf("decode gif: %v", err)
	}
	return g
}

func TestRenderAnimatesEveryFrame(t *testing.T) {
	dir := t.TempDir()
	src := frames(t, dir, 12, 640, 360)

	dest := filepath.Join(t.TempDir(), "clip.gif")
	if err := clip.Render(src, dest, clip.Options{}); err != nil {
		t.Fatalf("render: %v", err)
	}

	g := decode(t, dest)
	if len(g.Image) != 12 {
		t.Fatalf("animated %d frames, want 12", len(g.Image))
	}

	// Loops forever. A clip that plays once and freezes reads as a broken image
	// on a phone rather than as a short animation.
	if g.LoopCount != 0 {
		t.Fatalf("LoopCount is %d, want 0 (loop forever)", g.LoopCount)
	}

	// The playback rate, expressed in the hundredths GIF actually stores. Two
	// or more of them, because a browser silently rounds a delay of one up to
	// ten and plays the clip at a tenth of the rate it asked for.
	want := 100 / clip.FPS
	if want < 2 {
		t.Fatalf("clip.FPS of %d encodes to a delay of %d, which browsers clamp", clip.FPS, want)
	}
	for i, d := range g.Delay {
		if d != want {
			t.Fatalf("frame %d has delay %d, want %d", i, d, want)
		}
	}
}

func TestRenderClampsTheLongEdge(t *testing.T) {
	dir := t.TempDir()
	src := frames(t, dir, 4, 1920, 1080)

	dest := filepath.Join(t.TempDir(), "clip.gif")
	if err := clip.Render(src, dest, clip.Options{}); err != nil {
		t.Fatalf("render: %v", err)
	}

	// The animation's own size, rather than any one frame's: past the first,
	// frames cover only the part of the picture that changed.
	g := decode(t, dest)
	if g.Config.Width != clip.LongEdge {
		t.Fatalf("animation is %dx%d, want a long edge of %d",
			g.Config.Width, g.Config.Height, clip.LongEdge)
	}
}

// One canvas, whatever arrives. Frames of differing dimensions encode into an
// animation that jumps, and a frame reaching outside the canvas it declared is
// one a decoder is entitled to reject.
func TestRenderKeepsOneCanvas(t *testing.T) {
	dir := t.TempDir()
	src := frames(t, dir, 3, 640, 360)

	odd := filepath.Join(dir, "odd.jpg")
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 320, 240)), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode odd frame: %v", err)
	}
	if err := os.WriteFile(odd, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write odd frame: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "clip.gif")
	if err := clip.Render(append(src, odd), dest, clip.Options{}); err != nil {
		t.Fatalf("render: %v", err)
	}

	g := decode(t, dest)
	canvas := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	if got := g.Image[0].Bounds(); got != canvas {
		t.Fatalf("first frame is %v, want the whole canvas %v", got, canvas)
	}
	for i, img := range g.Image {
		if !img.Bounds().In(canvas) {
			t.Fatalf("frame %d is %v, which reaches outside the canvas %v", i, img.Bounds(), canvas)
		}
	}
}

// The palette is built from the clip rather than taken off the shelf, which is
// what keeps a face out of the dozen browns a fixed palette would allow it.
func TestRenderChoosesItsOwnColours(t *testing.T) {
	dir := t.TempDir()
	src := frames(t, dir, 4, 320, 240)

	dest := filepath.Join(t.TempDir(), "clip.gif")
	if err := clip.Render(src, dest, clip.Options{}); err != nil {
		t.Fatalf("render: %v", err)
	}

	g := decode(t, dest)
	pal, ok := g.Config.ColorModel.(color.Palette)
	if !ok {
		// Without a global table every frame repeats its own copy of the
		// palette, which is 768 bytes each on a clip a hundred frames long.
		t.Fatal("animation carries no global colour table")
	}
	// 255 colours and one transparent entry. A palette that came out short
	// means the median cut stopped dividing — see split, where a box holding
	// one dominant colour used to give up and take the whole palette with it.
	if len(pal) != clip.Colors+1 {
		t.Fatalf("palette holds %d entries, want %d", len(pal), clip.Colors+1)
	}

	// Transparency is checked on a frame rather than on the table above,
	// because that is where GIF keeps it: the file stores plain RGB and names
	// one index transparent per frame, and the decoder puts that back as an
	// entry with zero alpha.
	var clear int
	for _, c := range g.Image[1].Palette {
		if _, _, _, a := c.RGBA(); a == 0 {
			clear++
		}
	}
	if clear != 1 {
		// Without one, the pixels the differencing dropped draw as a colour
		// instead of letting the frame below show through.
		t.Fatalf("frame palette holds %d transparent entries, want exactly 1", clear)
	}
}

// Frames after the first carry only what changed, which is what pays for the
// resolution — and playing them back has to reconstruct the picture anyway.
func TestRenderSendsOnlyWhatChanged(t *testing.T) {
	dir := t.TempDir()

	// A still background with a small moving square, which is what a booth
	// actually films: one person against a wall that does not move.
	var src []string
	for i := range 8 {
		img := image.NewRGBA(image.Rect(0, 0, 320, 240))
		draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{R: 40, G: 90, B: 160, A: 255}}, image.Point{}, draw.Src)
		box := image.Rect(20+i*12, 100, 60+i*12, 140)
		draw.Draw(img, box, &image.Uniform{color.RGBA{R: 230, G: 180, B: 150, A: 255}}, image.Point{}, draw.Src)

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
			t.Fatalf("encode frame %d: %v", i, err)
		}
		p := filepath.Join(dir, clip.FrameName(i))
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
		src = append(src, p)
	}

	dest := filepath.Join(t.TempDir(), "clip.gif")
	if err := clip.Render(src, dest, clip.Options{}); err != nil {
		t.Fatalf("render: %v", err)
	}

	g := decode(t, dest)
	canvas := image.Rect(0, 0, g.Config.Width, g.Config.Height)
	full := canvas.Dx() * canvas.Dy()

	for i, img := range g.Image[1:] {
		if b := img.Bounds(); b.Dx()*b.Dy() > full/2 {
			t.Fatalf("frame %d covers %v of a %v canvas; nothing was dropped", i+1, b, canvas)
		}
	}

	// Every frame must be disposed by leaving it in place, because the frame on
	// top of it is mostly transparent and this is what shows through. Any other
	// disposal plays as a clip flashing to background between frames.
	for i, d := range g.Disposal {
		if d != gif.DisposalNone {
			t.Fatalf("frame %d has disposal %d, want DisposalNone (%d)", i, d, gif.DisposalNone)
		}
	}

	// And it still has to look like the footage. Composited the way a browser
	// does, the moving square has to be where it was filmed — a differencing
	// bug shows up here as a trail of stale boxes behind it.
	screen := image.NewRGBA(canvas)
	for i, frame := range g.Image {
		draw.Draw(screen, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)

		at := image.Pt(20+i*12+20, 120)
		if r, _, _, _ := screen.At(at.X, at.Y).RGBA(); r>>8 < 180 {
			t.Fatalf("frame %d: the square is missing at %v", i, at)
		}
		if i > 0 {
			// Where the square was two steps ago, which must be wall by now.
			behind := image.Pt(20+(i-2)*12+20, 120)
			if behind.X > 0 {
				if r, _, _, _ := screen.At(behind.X, behind.Y).RGBA(); r>>8 > 180 {
					t.Fatalf("frame %d: the square left a stale trail at %v", i, behind)
				}
			}
		}
	}
}

// One bad upload out of fifty is a clip a hundredth of a second shorter, not a
// customer handed nothing.
func TestRenderSkipsAFrameThatWillNotDecode(t *testing.T) {
	dir := t.TempDir()
	src := frames(t, dir, 5, 320, 240)

	junk := filepath.Join(dir, "junk.jpg")
	if err := os.WriteFile(junk, []byte("not a jpeg"), 0o644); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "clip.gif")
	if err := clip.Render(append(src, junk), dest, clip.Options{}); err != nil {
		t.Fatalf("render: %v", err)
	}

	if g := decode(t, dest); len(g.Image) != 5 {
		t.Fatalf("animated %d frames, want the 5 that decoded", len(g.Image))
	}
}

func TestRenderRefusesTooFewFrames(t *testing.T) {
	dir := t.TempDir()
	src := frames(t, dir, 1, 320, 240)

	dest := filepath.Join(t.TempDir(), "clip.gif")
	err := clip.Render(src, dest, clip.Options{})
	if !errors.Is(err, clip.ErrTooShort) {
		t.Fatalf("render: %v, want ErrTooShort", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("a refused render still wrote a file")
	}
}

// A render that fails must not leave the temporary file behind, or the next
// sweep finds a stray .part beside every clip it could not build.
func TestRenderLeavesNoPartFile(t *testing.T) {
	dir := t.TempDir()
	src := frames(t, dir, 3, 320, 240)

	out := t.TempDir()
	dest := filepath.Join(out, "clip.gif")
	if err := clip.Render(src, dest, clip.Options{}); err != nil {
		t.Fatalf("render: %v", err)
	}

	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".part" {
			t.Fatalf("left a temporary file behind: %s", e.Name())
		}
	}
}
