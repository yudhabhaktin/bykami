package frames

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The house frames are the only artwork in this repository whose cells are
// written down independently of this package — each one has a template.json the
// booth composes from, and they have been composed and printed. Detecting them
// is therefore the one test that checks inference against something other than a
// fixture this package made up.
//
// It reaches across module boundaries to read them, which nothing else here
// does. That is the point: these two modules have to agree about where photos
// go, and this is where the agreement is checked. The manifests were written
// from this detector's output, so a disagreement means one of them has moved
// without the other — re-exported artwork, or a change to the thresholds in
// detect.go — and the symptom either way is a face printed off its slot.
func TestDetectRecoversTheHouseFrames(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "agent", "internal", "compose", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("house frames not readable from here: %v", err)
	}

	var checked int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(dir, e.Name(), "template.json"))
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		var manifest struct {
			Overlay string `json:"overlay"`
			Cells   []Cell `json:"cells"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Errorf("%s: manifest: %v", e.Name(), err)
			continue
		}
		// A template may legitimately have no artwork over the photos — there is
		// nothing for the detector to read out of one that does not.
		if manifest.Overlay == "" {
			continue
		}

		t.Run(e.Name(), func(t *testing.T) {
			checked++

			art, err := os.ReadFile(filepath.Join(dir, e.Name(), manifest.Overlay))
			if err != nil {
				t.Fatalf("artwork: %v", err)
			}

			w, h, got, err := Detect(art)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if _, ok := sheets[[2]int{w, h}]; !ok {
				t.Fatalf("%dx%d is not a layout the catalogue accepts", w, h)
			}
			if len(got) != len(manifest.Cells) {
				t.Fatalf("got %d cells, the manifest has %d: %+v", len(got), len(manifest.Cells), got)
			}
			for i, want := range manifest.Cells {
				if got[i] != want {
					t.Errorf("cell %d = %+v, the manifest says %+v", i, got[i], want)
				}
			}
		})
	}

	if checked == 0 {
		t.Fatal("no house frame was checked, so this test proved nothing")
	}
}
