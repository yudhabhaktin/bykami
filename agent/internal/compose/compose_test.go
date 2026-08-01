package compose_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/bhaktiyudha/bykami/agent/internal/compose"
	"github.com/bhaktiyudha/bykami/agent/internal/printer"
)

func frame(t *testing.T, dir, name string, w, h int, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestBuiltinTemplatesLoad(t *testing.T) {
	all, err := compose.Builtin()
	if err != nil {
		t.Fatalf("builtin: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("the booth ships with no templates, so it has nothing to offer")
	}

	for _, tpl := range all {
		if len(tpl.Cells) == 0 {
			t.Errorf("template %q has no cells", tpl.ID)
		}
		if _, _, err := compose.SheetSize(tpl.Layout); err != nil {
			t.Errorf("template %q: %v", tpl.ID, err)
		}
	}
}

func TestSheetSizesAreThePrinterSpec(t *testing.T) {
	for _, tc := range []struct {
		layout printer.Layout
		w, h   int
	}{
		{printer.Layout4R, 1200, 1800},   // 4x6 at 300 dpi
		{printer.LayoutStrip, 600, 1800}, // 2x6
		{printer.Layout6x8, 1800, 2400},
	} {
		w, h, err := compose.SheetSize(tc.layout)
		if err != nil {
			t.Fatalf("%s: %v", tc.layout, err)
		}
		if w != tc.w || h != tc.h {
			t.Errorf("%s = %dx%d, want %dx%d", tc.layout, w, h, tc.w, tc.h)
		}
	}
}

func TestSheetComposesAtPrintResolution(t *testing.T) {
	all, err := compose.Builtin()
	if err != nil {
		t.Fatalf("builtin: %v", err)
	}

	var sheet compose.Template
	for _, tpl := range all {
		if tpl.ID == "gacoan-3-tiket" {
			sheet = tpl
		}
	}
	if sheet.ID == "" {
		t.Fatal("gacoan-3-tiket is missing")
	}

	// One colour per cell, so a photo drawn into the wrong slot is a wrong
	// colour rather than a passing test.
	want := []color.RGBA{
		{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255},
		{R: 255, G: 255, A: 255}, {R: 255, B: 255, A: 255}, {G: 255, B: 255, A: 255},
	}
	if len(want) != len(sheet.Cells) {
		t.Fatalf("template has %d cells, the test has %d colours", len(sheet.Cells), len(want))
	}

	dir := t.TempDir()
	var photos []string
	for i, c := range want {
		photos = append(photos, frame(t, dir, fmt.Sprintf("%d.jpg", i), 1800, 1200, c))
	}

	dest := filepath.Join(dir, "sheet.jpg")
	size, err := sheet.Sheet(photos, compose.Filter{}, dest)
	if err != nil {
		t.Fatalf("sheet: %v", err)
	}
	if size.X != 1200 || size.Y != 1800 {
		t.Fatalf("sheet is %v, want 1200x1800", size)
	}

	// And the cells really hold the photos, in order.
	f, err := os.Open(dest)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	for i, want := range want {
		c := sheet.Cells[i]
		got := img.At(c.X+c.W/2, c.Y+c.H/2)
		r, g, b, _ := got.RGBA()
		wr, wg, wb := uint32(want.R)<<8, uint32(want.G)<<8, uint32(want.B)<<8
		if !near(r, wr) || !near(g, wg) || !near(b, wb) {
			t.Errorf("cell %d is (%d,%d,%d), want about (%d,%d,%d)", i, r>>8, g>>8, b>>8, wr>>8, wg>>8, wb>>8)
		}
	}
}

// A letterboxed photo inside a designed frame reads as a mistake, so photos
// fill their cell and the overflow is centre-cropped.
func TestPhotosFillTheirCell(t *testing.T) {
	dir := t.TempDir()
	tplDir := filepath.Join(dir, "t")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A cell far more portrait than a 3:2 photo, so a "fit" would leave bars.
	manifest := `{"name":"Tall","layout":"strip2x6","cells":[{"x":0,"y":0,"w":600,"h":1800}]}`
	if err := os.WriteFile(filepath.Join(tplDir, "template.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	tpl, err := compose.LoadOne(os.DirFS(tplDir), "t")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	photo := frame(t, dir, "wide.jpg", 3000, 2000, color.RGBA{R: 200, G: 40, B: 40, A: 255})
	dest := filepath.Join(dir, "sheet.jpg")
	if _, err := tpl.Sheet([]string{photo}, compose.Filter{}, dest); err != nil {
		t.Fatalf("sheet: %v", err)
	}

	f, err := os.Open(dest)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Corners would be white if the photo had been letterboxed.
	for _, p := range []image.Point{{X: 5, Y: 5}, {X: 594, Y: 1794}} {
		r, g, b, _ := img.At(p.X, p.Y).RGBA()
		if near(r, 0xFFFF) && near(g, 0xFFFF) && near(b, 0xFFFF) {
			t.Fatalf("corner %v is white; the photo was letterboxed instead of filling the cell", p)
		}
	}
}

func TestOverlayIsDrawnOverThePhotos(t *testing.T) {
	dir := t.TempDir()
	tplDir := filepath.Join(dir, "t")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// An opaque black band across the top of the sheet, transparent elsewhere.
	overlay := image.NewRGBA(image.Rect(0, 0, 600, 1800))
	for y := range 100 {
		for x := range 600 {
			overlay.Set(x, y, color.RGBA{A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, overlay); err != nil {
		t.Fatalf("encode overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tplDir, "frame.png"), buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write overlay: %v", err)
	}

	manifest := `{"name":"Banded","layout":"strip2x6","overlay":"frame.png","cells":[{"x":0,"y":0,"w":600,"h":1800}]}`
	if err := os.WriteFile(filepath.Join(tplDir, "template.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	tpl, err := compose.LoadOne(os.DirFS(tplDir), "t")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	photo := frame(t, dir, "p.jpg", 1200, 1800, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	dest := filepath.Join(dir, "sheet.jpg")
	if _, err := tpl.Sheet([]string{photo}, compose.Filter{}, dest); err != nil {
		t.Fatalf("sheet: %v", err)
	}

	f, err := os.Open(dest)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	r, _, _, _ := img.At(300, 50).RGBA()
	if r > 0x4000 {
		t.Fatal("the overlay was not drawn over the photo")
	}
	r, _, _, _ = img.At(300, 900).RGBA()
	if r < 0xC000 {
		t.Fatal("the overlay's transparent area covered the photo")
	}
}

// A cell off the edge is a photo half-printed, caught when the template loads
// rather than when a customer prints it.
func TestCellOutsideTheSheetIsRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "template.json"),
		[]byte(`{"name":"Bad","layout":"strip2x6","cells":[{"x":0,"y":1700,"w":600,"h":400}]}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := compose.LoadOne(os.DirFS(dir), "bad"); err == nil {
		t.Fatal("accepted a cell that falls off the sheet")
	}
}

// A typo in a manifest is otherwise a field that silently does nothing, and the
// symptom is a photo in the wrong place on a printed sheet.
func TestUnknownManifestFieldIsRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "template.json"),
		[]byte(`{"name":"Typo","layout":"strip2x6","cel":[{"x":0,"y":0,"w":600,"h":1800}]}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := compose.LoadOne(os.DirFS(dir), "typo"); err == nil {
		t.Fatal("accepted a manifest with a misspelled field")
	}
}

func TestWrongPhotoCountIsRejected(t *testing.T) {
	all, err := compose.Builtin()
	if err != nil {
		t.Fatalf("builtin: %v", err)
	}
	var sheet compose.Template
	for _, tpl := range all {
		if tpl.ID == "gacoan-3-tiket" {
			sheet = tpl
		}
	}

	dir := t.TempDir()
	one := frame(t, dir, "a.jpg", 1200, 800, color.RGBA{A: 255})
	if _, err := sheet.Sheet([]string{one}, compose.Filter{}, filepath.Join(dir, "out.jpg")); err == nil {
		t.Fatal("composed a six-cell sheet from one photo")
	}
}

// Templates are content dropped into a folder, so a manifest that points
// outside its own directory is a mistake rather than an instruction.
func TestOverlayCannotEscapeTheTemplateDirectory(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret.png")
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(secret, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tplDir := filepath.Join(root, "t")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifest := `{"name":"Escape","layout":"strip2x6","overlay":"../secret.png","cells":[{"x":0,"y":0,"w":600,"h":1800}]}`
	if err := os.WriteFile(filepath.Join(tplDir, "template.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	tpl, err := compose.LoadOne(os.DirFS(tplDir), "t")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	photo := frame(t, root, "p.jpg", 1200, 1800, color.RGBA{A: 255})
	if _, err := tpl.Sheet([]string{photo}, compose.Filter{}, filepath.Join(root, "out.jpg")); err == nil {
		t.Fatal("read a file from outside the template directory")
	}
}

func near(got, want uint32) bool {
	d := int(got) - int(want)
	if d < 0 {
		d = -d
	}
	return d < 0x2000
}
