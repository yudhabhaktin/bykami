package clip_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/clip"
	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

var testPackage = session.Package{
	ID: "mini", Name: "Single Session", PriceIDR: 45000,
	TemplateID: "gacoan-1-taplak", PrintCopies: 1, TakeLimit: 15,
}

type fixture struct {
	clips    *clip.Store
	photos   *photo.Store
	sessions *session.Store
	session  session.Session
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	sessions := session.New(db)
	sess, err := sessions.Start(context.Background(), "jajag", testPackage)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	return fixture{
		clips:    clip.New(db),
		photos:   photo.New(db),
		sessions: sessions,
		session:  sess,
	}
}

// frame records a photo so a clip has something real to hang off. The foreign
// key is the point: a clip without a photo is a moment belonging to nothing.
func (f fixture) frame(t *testing.T, hash string) photo.Photo {
	t.Helper()

	p, err := f.photos.Record(context.Background(), photo.Photo{
		SessionID: f.session.ID, ContentHash: hash, Path: "sessions/" + hash + ".jpg",
		Bytes: 6, Width: 1920, Height: 1080, Source: photo.Webcam, CapturedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("record photo: %v", err)
	}
	return p
}

func (f fixture) record(t *testing.T, p photo.Photo, frames int) clip.Clip {
	t.Helper()

	c, err := f.clips.Record(context.Background(), clip.Clip{
		PhotoID: p.ID, SessionID: p.SessionID,
		Dir: clip.DirFor(p.SessionID, p.ID), Frames: frames, CapturedAt: p.CapturedAt,
	})
	if err != nil {
		t.Fatalf("record clip: %v", err)
	}
	return c
}

func TestRecordAndFindByPhoto(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	p := f.frame(t, "one")
	c := f.record(t, p, 50)

	got, err := f.clips.ByPhoto(ctx, p.ID)
	if err != nil {
		t.Fatalf("by photo: %v", err)
	}
	if got.ID != c.ID {
		t.Fatalf("found clip %s, want %s", got.ID, c.ID)
	}
	if got.Frames != 50 {
		t.Fatalf("recorded %d frames, want 50", got.Frames)
	}
	if got.Dir != "clips/"+f.session.ID+"/"+p.ID {
		t.Fatalf("clip filed at %q", got.Dir)
	}
}

// The burst is posted fire-and-forget after the shutter, so a client retrying a
// request whose response it never saw is the ordinary way this happens. It must
// not become a second copy of the same five seconds on disk.
func TestRecordRefusesASecondClipForOnePhoto(t *testing.T) {
	f := newFixture(t)

	p := f.frame(t, "one")
	f.record(t, p, 50)

	_, err := f.clips.Record(context.Background(), clip.Clip{
		PhotoID: p.ID, SessionID: p.SessionID,
		Dir: clip.DirFor(p.SessionID, p.ID), Frames: 50, CapturedAt: p.CapturedAt,
	})
	if !errors.Is(err, clip.ErrDuplicate) {
		t.Fatalf("record: %v, want ErrDuplicate", err)
	}
}

func TestUnrenderedSkipsFinishedAndPurgedClips(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	waiting := f.record(t, f.frame(t, "one"), 50)
	done := f.record(t, f.frame(t, "two"), 50)
	gone := f.record(t, f.frame(t, "three"), 50)

	if err := f.clips.SetGIF(ctx, done.ID, "clips/x/done.gif"); err != nil {
		t.Fatalf("set gif: %v", err)
	}
	// Purged, and never rendered. Its frames are gone and nothing will bring
	// them back, so a worker that kept offering it would retry forever.
	if err := f.clips.MarkPurged(ctx, gone.ID); err != nil {
		t.Fatalf("mark purged: %v", err)
	}

	due, err := f.clips.Unrendered(ctx, 10)
	if err != nil {
		t.Fatalf("unrendered: %v", err)
	}
	if len(due) != 1 || due[0].ID != waiting.ID {
		t.Fatalf("queue has %d clips, want only the unrendered one", len(due))
	}
}

func TestRenderedOnlyReturnsWhatAPhoneCanShow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	shown := f.frame(t, "one")
	building := f.frame(t, "two")
	purged := f.frame(t, "three")

	ready := f.record(t, shown, 50)
	f.record(t, building, 50)
	swept := f.record(t, purged, 50)

	if err := f.clips.SetGIF(ctx, ready.ID, "clips/x/ready.gif"); err != nil {
		t.Fatalf("set gif: %v", err)
	}
	// Rendered, then swept at retention. Listing it would put a broken image on
	// a customer's phone.
	if err := f.clips.SetGIF(ctx, swept.ID, "clips/x/swept.gif"); err != nil {
		t.Fatalf("set gif: %v", err)
	}
	if err := f.clips.MarkPurged(ctx, swept.ID); err != nil {
		t.Fatalf("mark purged: %v", err)
	}

	got, err := f.clips.Rendered(ctx, f.session.ID)
	if err != nil {
		t.Fatalf("rendered: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("returned %d clips, want 1", len(got))
	}
	if _, ok := got[shown.ID]; !ok {
		t.Fatal("the rendered clip is not keyed by its photo")
	}
}

// An orphan frame — a staff test shot, or one that missed its session by a
// second — still has somewhere to put its clip.
func TestPathsForAnOrphan(t *testing.T) {
	if got, want := clip.DirFor("", "abc"), "clips/unassigned/abc"; got != want {
		t.Fatalf("DirFor = %q, want %q", got, want)
	}
	if got, want := clip.GIFPathFor("", "abc"), "clips/unassigned/abc.gif"; got != want {
		t.Fatalf("GIFPathFor = %q, want %q", got, want)
	}
}

// Lexical order must be chronological. A shuffled clip is not a slower clip,
// it is a broken one.
func TestFrameNamesSortChronologically(t *testing.T) {
	if got, want := clip.FrameName(0), "0000.jpg"; got != want {
		t.Fatalf("FrameName(0) = %q, want %q", got, want)
	}
	if clip.FrameName(9) > clip.FrameName(10) {
		t.Fatal("frame 9 sorts after frame 10, so clips will play out of order")
	}
}
