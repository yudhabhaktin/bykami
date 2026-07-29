package frames

import (
	"os"
	"path/filepath"
	"testing"
)

// The house frames are the only artwork in this repository whose cells are
// known independently — they are written down in each template.json, drawn from
// the same numbers in design/frames/frame.html, and have been composed and
// printed. Detecting them is therefore the one test that checks inference
// against something other than a fixture this package made up.
//
// It reaches across module boundaries to read them, which nothing else here
// does. That is the point: these two modules have to agree about where photos
// go, and this is where the agreement is checked.
func TestDetectRecoversTheHouseFrames(t *testing.T) {
	// design/frames punches each hole 4px inside the manifest cell so the ink
	// ring laps the photo's outer edge. Detection sees the hole, so every cell
	// comes back inset by exactly that much — and if it ever does not, either
	// the artwork or the manifest has moved without the other.
	const inset = 4

	for _, tc := range []struct {
		id    string
		cells []Cell // as written in template.json
	}{
		{"strip-3", []Cell{
			{X: 30, Y: 36, W: 540, H: 450},
			{X: 30, Y: 516, W: 540, H: 450},
			{X: 30, Y: 996, W: 540, H: 450},
		}},
		{"strip-4", []Cell{
			{X: 30, Y: 30, W: 540, H: 360},
			{X: 30, Y: 405, W: 540, H: 360},
			{X: 30, Y: 780, W: 540, H: 360},
			{X: 30, Y: 1155, W: 540, H: 360},
		}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "agent", "internal", "compose",
				"templates", tc.id, "overlay.png")
			art, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("house frame not readable from here: %v", err)
			}

			w, h, got, err := Detect(art)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if w != 600 || h != 1800 {
				t.Fatalf("sheet = %dx%d, want 600x1800", w, h)
			}
			if len(got) != len(tc.cells) {
				t.Fatalf("got %d cells, want %d: %+v", len(got), len(tc.cells), got)
			}
			for i, c := range tc.cells {
				want := Cell{X: c.X + inset, Y: c.Y + inset, W: c.W - 2*inset, H: c.H - 2*inset}
				if got[i] != want {
					t.Errorf("cell %d = %+v, want %+v", i, got[i], want)
				}
			}

			if _, ok := sheets[[2]int{w, h}]; !ok {
				t.Errorf("%dx%d is not a layout the catalogue accepts", w, h)
			}
		})
	}
}
