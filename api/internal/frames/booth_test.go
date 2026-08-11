package frames

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

func newBooths(t *testing.T) (*Booths, *Catalogue) {
	t.Helper()
	cat, db := newCatalogue(t)
	return NewBooths(db), cat
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// The whole reason this table exists: a booth offers designs that are not in
// the catalogue, and the server has never seen their artwork.
func TestReportAsksForArtworkItDoesNotHave(t *testing.T) {
	b, _ := newBooths(t)
	art := strip3()

	want, err := b.Report(context.Background(), "jajag", []Design{
		{ID: "gacoan-1-taplak", Name: "Taplak Gacoan", Layout: Strip2x6, SHA256: sum(art)},
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(want) != 1 || want[0] != sum(art) {
		t.Fatalf("want = %v, want the one hash the server is missing", want)
	}

	if err := b.StoreArtwork(context.Background(), sum(art), art); err != nil {
		t.Fatalf("StoreArtwork: %v", err)
	}

	// The poll after that must cost nothing. This is the property that keeps a
	// five-minute report off a shop's internet connection.
	want, err = b.Report(context.Background(), "jajag", []Design{
		{ID: "gacoan-1-taplak", Name: "Taplak Gacoan", Layout: Strip2x6, SHA256: sum(art)},
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(want) != 0 {
		t.Errorf("want = %v, want nothing — the artwork is already stored", want)
	}
}

// A synced frame's bytes are already in the catalogue. Asking the booth to send
// them back is a download it pays for twice.
func TestReportDoesNotAskForArtworkTheCatalogueAlreadyHas(t *testing.T) {
	b, cat := newBooths(t)
	art := strip3()

	f, err := cat.Create(context.Background(), NewFrame{Name: "Wisuda 2026", PNG: art})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	want, err := b.Report(context.Background(), "jajag", []Design{
		{ID: f.ID, Name: f.Name, Layout: f.Layout, Cells: f.Cells, SHA256: f.SHA256},
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(want) != 0 {
		t.Fatalf("want = %v, but the catalogue already holds those bytes", want)
	}

	// And the console can still draw it, without the booth having uploaded
	// anything.
	got, err := b.Artwork(context.Background(), f.SHA256)
	if err != nil {
		t.Fatalf("Artwork: %v", err)
	}
	if !bytes.Equal(got, art) {
		t.Error("artwork read back from the catalogue is not the artwork stored")
	}
}

// The report is the whole set. A design missing from it is one the booth no
// longer offers, and the console must stop showing it.
func TestReportReplacesTheSet(t *testing.T) {
	b, _ := newBooths(t)
	ctx := context.Background()

	if _, err := b.Report(ctx, "jajag", []Design{
		{ID: "one", Name: "One", Layout: R4},
		{ID: "two", Name: "Two", Layout: R4},
	}); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if _, err := b.Report(ctx, "jajag", []Design{{ID: "two", Name: "Two", Layout: R4}}); err != nil {
		t.Fatalf("Report: %v", err)
	}

	all, err := b.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("booths = %d, want 1", len(all))
	}
	if len(all[0].Designs) != 1 || all[0].Designs[0].ID != "two" {
		t.Errorf("designs = %+v, want only \"two\" — \"one\" was withdrawn", all[0].Designs)
	}
}

// Artwork nothing lists any more goes, or a BLOB table only ever grows.
func TestReportPrunesArtworkNoBoothStillLists(t *testing.T) {
	b, _ := newBooths(t)
	ctx := context.Background()
	art := strip3()

	if _, err := b.Report(ctx, "jajag", []Design{{ID: "one", Name: "One", Layout: R4, SHA256: sum(art)}}); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if err := b.StoreArtwork(ctx, sum(art), art); err != nil {
		t.Fatalf("StoreArtwork: %v", err)
	}
	if _, err := b.Report(ctx, "jajag", nil); err != nil {
		t.Fatalf("Report: %v", err)
	}

	if _, err := b.Artwork(ctx, sum(art)); !errors.Is(err, ErrNoArtwork) {
		t.Errorf("Artwork err = %v, want ErrNoArtwork — nothing lists it", err)
	}
}

// Bytes that are not what the hash says are a truncated upload or a proxy that
// rewrote the body. Storing them puts artwork under a name that is not its own.
func TestStoreArtworkRefusesBytesThatAreNotTheirHash(t *testing.T) {
	b, _ := newBooths(t)
	err := b.StoreArtwork(context.Background(), sum([]byte("the frame")), []byte("something else"))
	if !errors.Is(err, ErrArtworkMismatch) {
		t.Errorf("err = %v, want ErrArtworkMismatch", err)
	}
}

// The outlet becomes a primary key and is rendered in the console. It arrives
// from the network, so it is judged rather than normalised.
func TestReportRefusesAnOutletThatIsNotASlug(t *testing.T) {
	b, _ := newBooths(t)
	for _, name := range []string{"", "Booth Y2K", "../etc", "jajag/../"} {
		if _, err := b.Report(context.Background(), name, nil); !errors.Is(err, ErrBadOutlet) {
			t.Errorf("Report(%q) err = %v, want ErrBadOutlet", name, err)
		}
	}
}

// A booth that offers nothing is a booth with a problem, and it has to be
// visible as one rather than absent from the page.
func TestABoothWithNoDesignsIsStillListed(t *testing.T) {
	b, _ := newBooths(t)
	if _, err := b.Report(context.Background(), "jajag", nil); err != nil {
		t.Fatalf("Report: %v", err)
	}
	all, err := b.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 1 || all[0].Outlet != "jajag" || len(all[0].Designs) != 0 {
		t.Errorf("all = %+v, want one booth reporting no designs", all)
	}
}
