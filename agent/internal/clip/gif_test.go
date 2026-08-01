package clip_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
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

	// Ten frames a second, expressed in the hundredths GIF actually stores.
	for i, d := range g.Delay {
		if d != 10 {
			t.Fatalf("frame %d has delay %d, want 10", i, d)
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

	g := decode(t, dest)
	for i, img := range g.Image {
		b := img.Bounds()
		if b.Dx() != clip.LongEdge {
			t.Fatalf("frame %d is %dx%d, want a long edge of %d", i, b.Dx(), b.Dy(), clip.LongEdge)
		}
	}
}

// Every frame at the same size, whatever arrives. Frames of differing
// dimensions encode into an animation that jumps.
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
	want := g.Image[0].Bounds()
	for i, img := range g.Image {
		if img.Bounds() != want {
			t.Fatalf("frame %d is %v, want %v", i, img.Bounds(), want)
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
