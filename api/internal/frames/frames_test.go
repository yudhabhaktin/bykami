package frames

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/store"
)

func newCatalogue(t *testing.T) (*Catalogue, *sql.DB) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db), db
}

func strip3() []byte {
	return frameWith(600, 1800, []Cell{
		{X: 30, Y: 36, W: 540, H: 450},
		{X: 30, Y: 516, W: 540, H: 450},
		{X: 30, Y: 996, W: 540, H: 450},
	})
}

func TestCreateReadsEverythingFromTheArtwork(t *testing.T) {
	c, _ := newCatalogue(t)
	f, err := c.Create(context.Background(), NewFrame{Name: "Wisuda 2026", Group: "wisuda", PNG: strip3()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	switch {
	case f.ID != "wisuda-2026":
		t.Errorf("id = %q, want wisuda-2026", f.ID)
	case f.Layout != Strip2x6:
		t.Errorf("layout = %q, want strip2x6 — chosen by the 600x1800 artwork", f.Layout)
	case len(f.Cells) != 3:
		t.Errorf("got %d cells, want 3", len(f.Cells))
	case f.Published:
		t.Error("a new frame is published; nobody has looked at the detected slots yet")
	case f.SHA256 == "":
		t.Error("no hash, so the booth cannot tell whether it already has the bytes")
	}
}

func TestCreateRejectsASheetSizeNothingPrints(t *testing.T) {
	c, _ := newCatalogue(t)
	// 4R's dimensions transposed: a real image, plausibly exported, and not a
	// size any of the three layouts describes.
	_, err := c.Create(context.Background(), NewFrame{
		Name: "Salah ukuran",
		PNG:  frameWith(1800, 1200, []Cell{{X: 100, Y: 100, W: 900, H: 900}}),
	})
	if !errors.Is(err, ErrSheetSize) {
		t.Fatalf("err = %v, want ErrSheetSize", err)
	}
}

func TestCreateRejectsADuplicateName(t *testing.T) {
	c, _ := newCatalogue(t)
	ctx := context.Background()
	if _, err := c.Create(ctx, NewFrame{Name: "Lebaran", PNG: strip3()}); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Different spelling, same slug — which is the collision that matters,
	// because the slug is the directory the booth writes into.
	_, err := c.Create(ctx, NewFrame{Name: "  lebaran  ", PNG: strip3()})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
}

// The reason seasons are dates rather than a switch: nobody has to remember.
func TestLiveRespectsTheSeasonWindow(t *testing.T) {
	c, _ := newCatalogue(t)
	ctx := context.Background()

	ramadan := time.Date(2027, 2, 8, 0, 0, 0, 0, time.UTC)
	syawal := time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC)

	f, err := c.Create(ctx, NewFrame{
		Name: "Ramadan 2027", Group: "musiman", PNG: strip3(),
		ActiveFrom: ramadan, ActiveUntil: syawal,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := c.SetPublished(ctx, f.ID, true); err != nil {
		t.Fatalf("SetPublished: %v", err)
	}

	for _, tc := range []struct {
		when time.Time
		want bool
		why  string
	}{
		{ramadan.AddDate(0, 0, -1), false, "the day before it opens"},
		{ramadan, true, "the first day"},
		{syawal.AddDate(0, 0, -1), true, "the last day"},
		{syawal, false, "the day it closes — Lebaran, and the frame is gone"},
		{syawal.AddDate(0, 5, 0), false, "five months later, with nobody having switched it off"},
	} {
		live, err := c.Live(ctx, tc.when)
		if err != nil {
			t.Fatalf("Live: %v", err)
		}
		if got := len(live) == 1; got != tc.want {
			t.Errorf("%s: live = %v, want %v", tc.why, got, tc.want)
		}
	}
}

func TestUnpublishedFramesNeverReachTheBooth(t *testing.T) {
	c, _ := newCatalogue(t)
	ctx := context.Background()
	if _, err := c.Create(ctx, NewFrame{Name: "Draf", PNG: strip3()}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	live, err := c.Live(ctx, time.Now())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("got %d live frames, want 0", len(live))
	}
	// But the operator can still see it, because it is the thing they came to
	// publish.
	all, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d frames in the console, want 1", len(all))
	}
}

func TestSetSeasonRejectsAWindowThatCannotOpen(t *testing.T) {
	c, _ := newCatalogue(t)
	ctx := context.Background()
	f, err := c.Create(ctx, NewFrame{Name: "Natal", PNG: strip3()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	from := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)
	if err := c.SetSeason(ctx, f.ID, from, from.AddDate(0, 0, -7)); !errors.Is(err, ErrBadWindow) {
		t.Fatalf("err = %v, want ErrBadWindow", err)
	}
}

// The id is joined onto a path by the agent. This is the check that no name can
// produce one that climbs out of the templates directory.
func TestSlugCannotEscapeADirectory(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Wisuda 2026", "wisuda-2026"},
		{"../../etc/passwd", "etc-passwd"},
		{"..", ""},
		{"C:\\Windows", "c-windows"},
		{"  Ramadan   Kareem  ", "ramadan-kareem"},
		{"Ulang Tahun ke-17!", "ulang-tahun-ke-17"},
		{"?????", ""},
	} {
		if got := slug(tc.name); got != tc.want {
			t.Errorf("slug(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestArtworkRoundTrips(t *testing.T) {
	c, _ := newCatalogue(t)
	ctx := context.Background()
	art := strip3()
	f, err := c.Create(ctx, NewFrame{Name: "Klasik", PNG: art})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, sum, err := c.Artwork(ctx, f.ID)
	if err != nil {
		t.Fatalf("Artwork: %v", err)
	}
	if sum != f.SHA256 {
		t.Errorf("hash = %q, want %q", sum, f.SHA256)
	}
	if len(got) != len(art) {
		t.Fatalf("got %d bytes, want %d", len(got), len(art))
	}
	// Byte-identical, because this is what the printer eventually composes
	// with. A re-encode anywhere in the path would be quality lost silently.
	for i := range art {
		if got[i] != art[i] {
			t.Fatalf("artwork differs at byte %d", i)
		}
	}
}
