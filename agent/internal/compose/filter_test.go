package compose_test

import (
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/bhaktiyudha/bykami/agent/internal/compose"
)

// oneCell is a 4R template with a single inset cell, so a test can look at a
// pixel inside the photo and another outside it.
func oneCell(t *testing.T, dir string, background bool) compose.Template {
	t.Helper()
	tplDir := filepath.Join(dir, "tpl")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := `{"name":"One","layout":"4r","cells":[{"x":100,"y":100,"w":1000,"h":1600}]}`
	if background {
		// A saturated background, so a filter that leaked outside the cell is
		// visible as a desaturated corner rather than as nothing.
		frame(t, tplDir, "bg.jpg", 1200, 1800, color.RGBA{R: 20, G: 200, B: 60, A: 255})
		manifest = `{"name":"One","layout":"4r","background":"bg.jpg",` +
			`"cells":[{"x":100,"y":100,"w":1000,"h":1600}]}`
	}
	if err := os.WriteFile(filepath.Join(tplDir, "template.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	tpl, err := compose.LoadOne(os.DirFS(tplDir), "one")
	if err != nil {
		t.Fatalf("load template: %v", err)
	}
	return tpl
}

// pixelAt reads one pixel out of a composed sheet.
func pixelAt(t *testing.T, path string, x, y int) color.RGBA {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

// close8 allows for JPEG quantisation. The sheet is encoded at quality 95,
// which moves a flat colour by a couple of levels, not by ten.
func close8(a, b uint8, tol int) bool {
	d := int(a) - int(b)
	return d <= tol && d >= -tol
}

// The test that matters: the filter has to reach the printed sheet. A filter
// applied only in CSS produces exactly this test failing while the screen looks
// right, and the customer finds out on paper.
func TestFilterReachesTheComposedSheet(t *testing.T) {
	dir := t.TempDir()
	tpl := oneCell(t, dir, false)

	// A saturated red photo. Grayscale is the easiest filter to check without
	// restating its own arithmetic: whatever comes out, R, G and B must agree.
	src := frame(t, dir, "red.jpg", 800, 800, color.RGBA{R: 220, G: 40, B: 40, A: 255})

	plain := filepath.Join(dir, "plain.jpg")
	if _, err := tpl.Sheet([]string{src}, compose.FilterByID(compose.NoFilter), plain); err != nil {
		t.Fatalf("compose plain: %v", err)
	}
	mono := filepath.Join(dir, "mono.jpg")
	if _, err := tpl.Sheet([]string{src}, compose.FilterByID("hitam-putih"), mono); err != nil {
		t.Fatalf("compose mono: %v", err)
	}

	// Well inside the single cell, away from any edge.
	x, y := 600, 900
	before := pixelAt(t, plain, x, y)
	after := pixelAt(t, mono, x, y)

	if !close8(before.R, 220, 6) || !close8(before.G, 40, 6) {
		t.Fatalf("unfiltered sheet is not the source colour: %+v", before)
	}
	if close8(after.R, before.R, 6) {
		t.Errorf("the filter did not reach the sheet: still %+v", after)
	}
	if !close8(after.R, after.G, 3) || !close8(after.G, after.B, 3) {
		t.Errorf("grayscale left a colour cast: %+v", after)
	}

	// Rec. 709 luminance of the source, which is what the matrix says it is.
	luma := 0.2126*220 + 0.7152*40 + 0.0722*40
	want := uint8(luma + 0.5)
	if !close8(after.R, want, 6) {
		t.Errorf("luma = %d, want about %d", after.R, want)
	}
}

// The frame is the designer's artwork and its colours were chosen. Filtering
// the whole sheet is the easy mistake, and it tints the frame too.
func TestFilterLeavesTheFrameArtworkAlone(t *testing.T) {
	dir := t.TempDir()
	tpl := oneCell(t, dir, true)

	src := frame(t, dir, "red.jpg", 800, 800, color.RGBA{R: 220, G: 40, B: 40, A: 255})
	out := filepath.Join(dir, "out.jpg")
	if _, err := tpl.Sheet([]string{src}, compose.FilterByID("hitam-putih"), out); err != nil {
		t.Fatalf("compose: %v", err)
	}

	// Outside the cell: the background, which must still be its own colour.
	bg := pixelAt(t, out, 20, 20)
	if close8(bg.R, bg.G, 8) && close8(bg.G, bg.B, 8) {
		t.Errorf("the background was desaturated along with the photo: %+v", bg)
	}
}

func TestFilterByIDFallsBackRatherThanFailing(t *testing.T) {
	// A booth that does not have the filter the kiosk asked for should print an
	// unfiltered sheet, not refuse at the till.
	if got := compose.FilterByID("no-such-filter"); got.ID != compose.NoFilter || got.Matrix != nil {
		t.Errorf("unknown filter = %+v, want the identity filter", got)
	}
	if got := compose.FilterByID(""); got.ID != compose.NoFilter {
		t.Errorf("empty filter = %q, want %q", got.ID, compose.NoFilter)
	}
}

// The catalogue is served to the browser, which applies these very numbers via
// feColorMatrix. A row of the wrong length would silently do nothing there.
func TestEveryFilterIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range compose.Filters {
		if f.ID == "" || f.Name == "" {
			t.Errorf("filter %+v has no id or no name", f)
		}
		if seen[f.ID] {
			t.Errorf("two filters share the id %q", f.ID)
		}
		seen[f.ID] = true

		if f.Matrix == nil {
			continue
		}
		// The alpha row must be identity. A filter that alters alpha would
		// produce a transparent region on a sheet that is about to be encoded
		// as JPEG, which has no alpha — it would print black.
		m := *f.Matrix
		if m[15] != 0 || m[16] != 0 || m[17] != 0 || m[18] != 1 || m[19] != 0 {
			t.Errorf("filter %q changes alpha; JPEG has none and it would print black", f.ID)
		}
	}
	if compose.Filters[0].ID != compose.NoFilter {
		t.Errorf("the first filter is %q, want %q — the kiosk shows it selected by default",
			compose.Filters[0].ID, compose.NoFilter)
	}
}
