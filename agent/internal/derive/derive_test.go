package derive_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/bhaktiyudha/bykami/agent/internal/derive"
)

func source(t *testing.T, dir string, w, h int, withEXIF bool) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	b := buf.Bytes()
	if withEXIF {
		b = injectAPP1(t, b)
	}

	path := filepath.Join(dir, "src.jpg")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// injectAPP1 splices a minimal EXIF segment in after SOI, which is where a
// camera puts it. Go's encoder never writes one, so the only way to prove the
// derivative strips EXIF is to start from a file that has some.
func injectAPP1(t *testing.T, jpg []byte) []byte {
	t.Helper()
	if len(jpg) < 2 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
		t.Fatal("source is not a JPEG")
	}

	payload := append([]byte("Exif\x00\x00"), []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x00")...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte((len(payload) + 2) & 0xFF)}
	seg = append(seg, payload...)

	out := make([]byte, 0, len(jpg)+len(seg))
	out = append(out, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

func TestFileClampsTheLongEdge(t *testing.T) {
	dir := t.TempDir()
	src := source(t, dir, 3000, 2000, false)
	dest := filepath.Join(dir, "derived", "out.jpg")

	size, err := derive.File(src, dest, derive.Options{})
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if size.X != derive.LongEdge {
		t.Fatalf("long edge = %d, want %d", size.X, derive.LongEdge)
	}
	// 3:2 preserved. A stretched delivery is worse than a large one.
	if want := derive.LongEdge * 2000 / 3000; size.Y != want {
		t.Fatalf("short edge = %d, want %d", size.Y, want)
	}

	// And it is smaller, which is the entire point.
	in, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat src: %v", err)
	}
	out, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat dest: %v", err)
	}
	if out.Size() >= in.Size() {
		t.Fatalf("derivative is %d bytes against an original of %d", out.Size(), in.Size())
	}
}

func TestFileClampsThePortraitLongEdge(t *testing.T) {
	dir := t.TempDir()
	src := source(t, dir, 2000, 3000, false)

	size, err := derive.File(src, filepath.Join(dir, "out.jpg"), derive.Options{})
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if size.Y != derive.LongEdge {
		t.Fatalf("portrait long edge = %d, want %d", size.Y, derive.LongEdge)
	}
}

// The privacy property, guarded. Without this the EXIF claim in the design
// record is a comment rather than a fact: every file a customer forwards to a
// WhatsApp group would carry the camera's serial number.
func TestDerivativeCarriesNoEXIF(t *testing.T) {
	dir := t.TempDir()
	src := source(t, dir, 1200, 800, true)

	// The premise: the source really does have EXIF.
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer in.Close()
	has, err := derive.HasEXIF(in)
	if err != nil {
		t.Fatalf("scan src: %v", err)
	}
	if !has {
		t.Fatal("test fixture has no EXIF, so it proves nothing")
	}

	dest := filepath.Join(dir, "out.jpg")
	if _, err := derive.File(src, dest, derive.Options{}); err != nil {
		t.Fatalf("derive: %v", err)
	}

	out, err := os.Open(dest)
	if err != nil {
		t.Fatalf("open dest: %v", err)
	}
	defer out.Close()
	has, err = derive.HasEXIF(out)
	if err != nil {
		t.Fatalf("scan dest: %v", err)
	}
	if has {
		t.Fatal("the delivered file still carries EXIF")
	}
}

// Upscaling adds bytes and no detail, and the strip cells in design/kiosk.md
// are well under the bound.
func TestSmallImageIsNotUpscaled(t *testing.T) {
	dir := t.TempDir()
	src := source(t, dir, 600, 540, false)

	size, err := derive.File(src, filepath.Join(dir, "out.jpg"), derive.Options{})
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if size.X != 600 || size.Y != 540 {
		t.Fatalf("size = %v, want 600x540 untouched", size)
	}
}

func TestFileLeavesNoPartialOnFailure(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "not-a-jpeg.jpg")
	if err := os.WriteFile(bad, []byte("this is not a JPEG"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	dest := filepath.Join(dir, "out.jpg")
	if _, err := derive.File(bad, dest, derive.Options{}); err == nil {
		t.Fatal("decoded a file that is not a JPEG")
	}
	for _, p := range []string{dest, dest + ".part"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("left %s behind after a failure", filepath.Base(p))
		}
	}
}

func TestResizeIsExact(t *testing.T) {
	for _, tc := range []struct {
		name         string
		w, h         int
		bound        int
		wantW, wantH int
	}{
		// The first row is the 200D's real output; the rest are the shapes
		// that break naive ratio arithmetic.
		{"landscape", 6000, 4000, 2048, 2048, 1365},
		{"portrait", 4000, 6000, 2048, 1365, 2048},
		{"square", 2000, 2000, 1024, 1024, 1024},
		{"already small", 800, 600, 2048, 800, 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := derive.Resize(image.NewRGBA(image.Rect(0, 0, tc.w, tc.h)), tc.bound).Bounds().Size()
			if got.X != tc.wantW || got.Y != tc.wantH {
				t.Fatalf("got %dx%d, want %dx%d", got.X, got.Y, tc.wantW, tc.wantH)
			}
		})
	}
}
