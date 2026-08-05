package clip_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bhaktiyudha/bykami/agent/internal/clip"
	"github.com/bhaktiyudha/bykami/agent/internal/compose"
)

// sixCell returns a real built-in design, because the thing worth testing about
// an animated sheet is how it behaves against artwork somebody actually drew:
// a synthetic template with no overlay would skip the alpha composite that runs
// once per frame and dominates the output's size.
func sixCell(t *testing.T) compose.Template {
	t.Helper()

	ts, err := compose.Builtin()
	if err != nil {
		t.Fatalf("builtin templates: %v", err)
	}
	for _, tpl := range ts {
		if len(tpl.Cells) == 6 && tpl.Overlay != "" {
			return tpl
		}
	}
	t.Fatal("no built-in six-cell design with artwork to test against")
	return compose.Template{}
}

// sources builds one clip per cell, each n frames long, plus a still.
func sources(t *testing.T, cells, n int) []clip.SheetSource {
	t.Helper()

	out := make([]clip.SheetSource, cells)
	for i := range out {
		dir := t.TempDir()
		fs := frames(t, dir, n, 400, 300)
		out[i] = clip.SheetSource{Frames: fs, Still: fs[0]}
	}
	return out
}

func TestRenderSheetAnimatesEveryCellAtOnce(t *testing.T) {
	tpl := sixCell(t)
	dest := filepath.Join(t.TempDir(), "sheet.gif")

	if err := clip.RenderSheet(tpl, sources(t, 6, 10), compose.FilterByID("asli"), dest, clip.Options{}); err != nil {
		t.Fatalf("render sheet: %v", err)
	}

	// Half the captured frames, because the sheet plays at SheetFPS while the
	// burst was grabbed at FPS — the same seconds, at a rate that suits a cell
	// a quarter of this size.
	g := decode(t, dest)
	if len(g.Image) != 5 {
		t.Fatalf("animated %d frames, want 5 — every other one of 10 captured", len(g.Image))
	}

	// The sheet's own proportions, reduced. A moving version shaped differently
	// from the paper is not the same picture.
	w, h, err := compose.SheetSize(tpl.Layout)
	if err != nil {
		t.Fatalf("sheet size: %v", err)
	}
	got := g.Image[0].Bounds()
	if got.Dy() != clip.SheetLongEdge {
		t.Fatalf("long edge is %d, want %d", got.Dy(), clip.SheetLongEdge)
	}
	if want := clip.SheetLongEdge * w / h; got.Dx() != want {
		t.Fatalf("sheet is %dx%d, want %dx%d for a %s", got.Dx(), got.Dy(), want, clip.SheetLongEdge, tpl.Layout)
	}
}

// The shortest clip sets the length. A cell that ran out while the others kept
// moving would read as a booth that froze, not as a clip that ended.
func TestRenderSheetRunsForTheShortestClip(t *testing.T) {
	tpl := sixCell(t)
	cells := sources(t, 6, 10)
	cells[3].Frames = cells[3].Frames[:4]

	dest := filepath.Join(t.TempDir(), "sheet.gif")
	if err := clip.RenderSheet(tpl, cells, compose.FilterByID("asli"), dest, clip.Options{}); err != nil {
		t.Fatalf("render sheet: %v", err)
	}

	if g := decode(t, dest); len(g.Image) != 2 {
		t.Fatalf("animated %d frames, want 2 — every other one of the shortest cell's 4", len(g.Image))
	}
}

// A cell whose burst never arrived holds its photograph while the rest move.
// Refusing the whole sheet over one missing clip would cost the customer five
// moving faces to avoid showing them a still one.
func TestRenderSheetKeepsStillCellsStill(t *testing.T) {
	tpl := sixCell(t)
	cells := sources(t, 6, 10)
	cells[0].Frames = nil
	cells[5].Frames = nil

	dest := filepath.Join(t.TempDir(), "sheet.gif")
	if err := clip.RenderSheet(tpl, cells, compose.FilterByID("asli"), dest, clip.Options{}); err != nil {
		t.Fatalf("render sheet: %v", err)
	}

	if g := decode(t, dest); len(g.Image) != 5 {
		t.Fatalf("animated %d frames, want 5 — every other one of 10 captured", len(g.Image))
	}
}

// Nothing moving is not an animation. The page already has the JPEG, and a GIF
// of six frozen faces is that JPEG at thirty times the size.
func TestRenderSheetRefusesASheetThatDoesNotMove(t *testing.T) {
	tpl := sixCell(t)
	cells := sources(t, 6, 10)
	for i := range cells {
		cells[i].Frames = nil
	}

	dest := filepath.Join(t.TempDir(), "sheet.gif")
	err := clip.RenderSheet(tpl, cells, compose.FilterByID("asli"), dest, clip.Options{})
	if !errors.Is(err, clip.ErrTooShort) {
		t.Fatalf("render sheet: %v, want ErrTooShort", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("a sheet that does not move still wrote a file")
	}
}

func TestRenderSheetRefusesTheWrongNumberOfCells(t *testing.T) {
	tpl := sixCell(t)
	dest := filepath.Join(t.TempDir(), "sheet.gif")

	err := clip.RenderSheet(tpl, sources(t, 3, 10), compose.FilterByID("asli"), dest, clip.Options{})
	if !errors.Is(err, compose.ErrCellCount) {
		t.Fatalf("render sheet: %v, want ErrCellCount", err)
	}
}

// What the customer's phone actually pulls, and the reason SheetLongEdge is the
// number it is.
//
// This travels over Indonesian mobile data, and a five-second sheet is a
// hundred captured frames of it. The ceiling is what makes the constants a
// decision rather than a guess: raising the long edge or the rate without
// measuring will fail here first, and the figure this logs is the one quoted in
// the constant's comment.
//
// Fed at the rate the kiosk actually captures — CLIP_FPS over five seconds —
// rather than at a round number, because that is the input the booth will hand
// this and the whole point of a ceiling is to be measured against reality. At
// the capture rate with no decimation this lands at 4.5 MB, which is what
// SheetFPS exists to bring back under.
func TestAnimatedSheetStaysUnderTheDeliveryCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("renders a hundred frames across six cells")
	}

	tpl := sixCell(t)
	dest := filepath.Join(t.TempDir(), "sheet.gif")

	captured := 5 * clip.FPS
	if err := clip.RenderSheet(tpl, sources(t, 6, captured), compose.FilterByID("asli"), dest, clip.Options{}); err != nil {
		t.Fatalf("render sheet: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat sheet: %v", err)
	}

	const ceiling = 4 << 20
	t.Logf("a five-second six-cell sheet at %d px is %.1f MB", clip.SheetLongEdge, float64(info.Size())/(1<<20))
	if info.Size() > ceiling {
		t.Fatalf("the animated sheet is %.1f MB, over the %d MB a phone should be asked for",
			float64(info.Size())/(1<<20), ceiling>>20)
	}
}
