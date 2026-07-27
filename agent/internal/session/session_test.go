package session_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

// clock is a hand-cranked time source. Attribution is entirely about time, so
// a test that cannot control the clock has to sleep through a 20-second grace
// window to prove anything about it.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testPackage is a catalogue entry standing in for whatever the customer taps.
var testPackage = session.Package{
	ID: "mini", Name: "MINI", PriceIDR: 45_000,
	TemplateID: "strip-3", PrintCopies: 1, TakeLimit: 15,
}

func setup(t *testing.T) (*sql.DB, *session.Store, *clock) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	c := newClock()
	return db, session.New(db, session.WithClock(c.now), session.WithGrace(20*time.Second)), c
}

// start opens a session and pays for it, which is what every test that is not
// about the payment gate wants.
func start(t *testing.T, sessions *session.Store, takeLimit int) session.Session {
	t.Helper()
	ctx := context.Background()

	pkg := testPackage
	pkg.TakeLimit = takeLimit

	s, err := sessions.Start(ctx, "jajag", pkg)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	s, err = sessions.MarkPaid(ctx, s.ID)
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	return s
}

func TestStartRefusesASecondSession(t *testing.T) {
	_, sessions, _ := setup(t)
	ctx := context.Background()

	if _, err := sessions.Start(ctx, "jajag", testPackage); err != nil {
		t.Fatalf("first start: %v", err)
	}

	// Not "close the old one and open a new one". An implicit close would hand
	// one customer's remaining frames to the next — and here it would also take
	// the booth from someone mid-payment.
	if _, err := sessions.Start(ctx, "jajag", testPackage); !errors.Is(err, session.ErrAlreadyOpen) {
		t.Fatalf("want ErrAlreadyOpen, got %v", err)
	}
}

// The self-service gate. Nobody stands between a stranger and the camera, so
// the payment is the attendant.
func TestShutterIsLockedUntilPaid(t *testing.T) {
	_, sessions, _ := setup(t)
	ctx := context.Background()

	s, err := sessions.Start(ctx, "jajag", testPackage)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if s.State != session.AwaitingPayment {
		t.Fatalf("a new session was %q, want awaiting_payment", s.State)
	}

	if _, err := sessions.MayFire(ctx); !errors.Is(err, session.ErrNotPaid) {
		t.Fatalf("the shutter fired without payment: %v", err)
	}

	paid, err := sessions.MarkPaid(ctx, s.ID)
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	switch {
	case paid.State != session.Open:
		t.Fatalf("state after payment = %q", paid.State)
	case paid.PaidAt.IsZero():
		t.Fatal("paid_at was not recorded")
	}

	if _, err := sessions.MayFire(ctx); err != nil {
		t.Fatalf("the shutter is still locked after payment: %v", err)
	}
}

// The kiosk polls for settlement, so the same answer arrives more than once by
// design.
func TestMarkPaidIsIdempotent(t *testing.T) {
	_, sessions, _ := setup(t)
	ctx := context.Background()

	s, err := sessions.Start(ctx, "jajag", testPackage)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	first, err := sessions.MarkPaid(ctx, s.ID)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := sessions.MarkPaid(ctx, s.ID)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !first.PaidAt.Equal(second.PaidAt) {
		t.Fatal("a second poll moved the payment time")
	}
}

// A customer who walks away from the QR code must not hold the booth forever.
func TestAbandonFreesTheBooth(t *testing.T) {
	_, sessions, _ := setup(t)
	ctx := context.Background()

	s, err := sessions.Start(ctx, "jajag", testPackage)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := sessions.Abandon(ctx, s.ID); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if _, err := sessions.Start(ctx, "jajag", testPackage); err != nil {
		t.Fatalf("the booth was still held: %v", err)
	}
}

// Money moved, so the row has to say so. A paid session is closed, never
// deleted.
func TestAbandonRefusesAPaidSession(t *testing.T) {
	_, sessions, _ := setup(t)
	ctx := context.Background()

	s := start(t, sessions, 15)
	if err := sessions.Abandon(ctx, s.ID); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("deleted a session that was paid for: %v", err)
	}
}

func TestStartCarriesThePackageOntoTheSession(t *testing.T) {
	_, sessions, _ := setup(t)

	s := start(t, sessions, 15)
	got, err := sessions.Get(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	switch {
	case got.Package.ID != testPackage.ID:
		t.Fatalf("package id = %q", got.Package.ID)
	case got.Package.PriceIDR != testPackage.PriceIDR:
		// Copied, not referenced: the catalogue will change and this row has to
		// still say what was sold.
		t.Fatalf("price = %d, want %d", got.Package.PriceIDR, testPackage.PriceIDR)
	case got.Package.PrintCopies != testPackage.PrintCopies:
		t.Fatalf("print copies = %d", got.Package.PrintCopies)
	}
}

func TestStartDefaultsTheTakeLimit(t *testing.T) {
	_, sessions, _ := setup(t)

	pkg := testPackage
	pkg.TakeLimit = 0
	s, err := sessions.Start(context.Background(), "jajag", pkg)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if s.TakeLimit != session.DefaultTakeLimit {
		t.Fatalf("take limit = %d, want %d", s.TakeLimit, session.DefaultTakeLimit)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	_, sessions, _ := setup(t)
	ctx := context.Background()

	s := start(t, sessions, 15)
	// The kiosk can send "Selesai" twice, and an idle timeout can close a
	// session the customer already closed.
	for i := range 2 {
		if err := sessions.Close(ctx, s.ID); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
	if _, open, err := sessions.Current(ctx); err != nil || open {
		t.Fatalf("still open after close: open=%v err=%v", open, err)
	}
}

// The grace window is the difference between a customer receiving the frame
// they paid for and it becoming an orphan nobody looks at.
func TestAttribute(t *testing.T) {
	_, sessions, c := setup(t)
	ctx := context.Background()

	// A frame that lands before anything is open is a staff test shot.
	if _, ok, err := sessions.Attribute(ctx, c.now()); err != nil || ok {
		t.Fatalf("attributed a frame with no session: ok=%v err=%v", ok, err)
	}

	s := start(t, sessions, 15)

	c.advance(5 * time.Second)
	id, ok, err := sessions.Attribute(ctx, c.now())
	if err != nil || !ok || id != s.ID {
		t.Fatalf("during session: id=%q ok=%v err=%v", id, ok, err)
	}

	if err := sessions.Close(ctx, s.ID); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A straggler: fired just before "Selesai", written just after.
	c.advance(10 * time.Second)
	id, ok, err = sessions.Attribute(ctx, c.now())
	if err != nil || !ok || id != s.ID {
		t.Fatalf("inside grace window: id=%q ok=%v err=%v", id, ok, err)
	}

	// Past the window it is an orphan, which is the honest answer: by now the
	// next customer could be in the room.
	c.advance(30 * time.Second)
	if _, ok, err := sessions.Attribute(ctx, c.now()); err != nil || ok {
		t.Fatalf("past the grace window: ok=%v err=%v", ok, err)
	}
}

// A frame written before the session opened cannot belong to it, even while it
// is the only session there is.
func TestAttributeIgnoresFramesOlderThanTheSession(t *testing.T) {
	_, sessions, c := setup(t)
	ctx := context.Background()

	before := c.now()
	c.advance(time.Minute)
	start(t, sessions, 15)

	if _, ok, err := sessions.Attribute(ctx, before); err != nil || ok {
		t.Fatalf("attributed a frame captured before the session opened: ok=%v err=%v", ok, err)
	}
}

// The open session wins over a closed one still inside its grace window. The
// alternative gives the previous customer's session a frame taken by the one
// currently in the room.
func TestAttributePrefersTheOpenSession(t *testing.T) {
	_, sessions, c := setup(t)
	ctx := context.Background()

	first := start(t, sessions, 15)
	c.advance(time.Second)
	if err := sessions.Close(ctx, first.ID); err != nil {
		t.Fatalf("close first: %v", err)
	}

	c.advance(2 * time.Second)
	second := start(t, sessions, 15)

	c.advance(time.Second)
	id, ok, err := sessions.Attribute(ctx, c.now())
	if err != nil || !ok {
		t.Fatalf("attribute: ok=%v err=%v", ok, err)
	}
	if id != second.ID {
		t.Fatal("a frame taken during the second session was given to the first")
	}
}

func TestMayFireStopsAtTheTakeLimit(t *testing.T) {
	db, sessions, c := setup(t)
	ctx := context.Background()
	photos := photo.NewWithClock(db, c.now)

	if _, err := sessions.MayFire(ctx); !errors.Is(err, session.ErrNoSession) {
		t.Fatalf("want ErrNoSession with nothing open, got %v", err)
	}

	s := start(t, sessions, 3)

	for i := range 3 {
		if _, err := sessions.MayFire(ctx); err != nil {
			t.Fatalf("fire %d refused: %v", i, err)
		}
		record(t, photos, s.ID, string(rune('a'+i)), c.now())
	}

	// The app owns the shutter now, so this is enforceable at capture rather
	// than merely counted afterwards.
	if _, err := sessions.MayFire(ctx); !errors.Is(err, session.ErrTakeLimit) {
		t.Fatalf("want ErrTakeLimit at the limit, got %v", err)
	}

	n, err := sessions.Takes(ctx, s.ID)
	if err != nil || n != 3 {
		t.Fatalf("takes = %d (err %v), want 3", n, err)
	}
}

func TestRecordDeliveryNeedsConsent(t *testing.T) {
	_, sessions, _ := setup(t)
	ctx := context.Background()

	s := start(t, sessions, 15)

	// Two purposes, therefore two consents. A number with no consent version is
	// "they agreed to something at some point", which is not a record.
	if err := sessions.RecordDelivery(ctx, s.ID, "+6281234567890", "", false); err == nil {
		t.Fatal("stored a number with no consent version")
	}

	if err := sessions.RecordDelivery(ctx, s.ID, "+6281234567890", "kiosk-consent-v1", true); err != nil {
		t.Fatalf("record delivery: %v", err)
	}

	got, err := sessions.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	switch {
	case got.Phone != "+6281234567890":
		t.Fatalf("phone = %q", got.Phone)
	case got.ConsentVersion != "kiosk-consent-v1":
		t.Fatalf("consent version = %q", got.ConsentVersion)
	case got.ConsentedAt.IsZero():
		t.Fatal("consented_at was not recorded")
	case !got.MarketingConsent:
		t.Fatal("the optional consent was not recorded separately")
	}
}

func TestRecordDeliveryDefaultsMarketingToNo(t *testing.T) {
	_, sessions, _ := setup(t)
	ctx := context.Background()

	s := start(t, sessions, 15)
	if err := sessions.RecordDelivery(ctx, s.ID, "+6281234567890", "kiosk-consent-v1", false); err != nil {
		t.Fatalf("record delivery: %v", err)
	}

	got, err := sessions.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.MarketingConsent {
		t.Fatal("declining promotional messages was recorded as accepting them")
	}
}

func record(t *testing.T, photos *photo.Store, sessionID, hash string, at time.Time) {
	t.Helper()
	if _, err := photos.Record(context.Background(), photo.Photo{
		SessionID:   sessionID,
		ContentHash: hash,
		Path:        "sessions/" + sessionID + "/" + hash + ".jpg",
		Bytes:       1,
		Width:       6000,
		Height:      4000,
		Source:      photo.HotFolder,
		CapturedAt:  at,
	}); err != nil {
		t.Fatalf("record photo: %v", err)
	}
}

// A photobooth fires faster than captured_at's one-second resolution, so
// several frames routinely share a timestamp. Ordering by the random id would
// shuffle them, and the order is the customer's strip.
func TestFramesInTheSameSecondKeepCaptureOrder(t *testing.T) {
	db, sessions, c := setup(t)
	ctx := context.Background()
	photos := photo.NewWithClock(db, c.now)

	s := start(t, sessions, 15)

	// The clock does not advance: every frame lands in the same second.
	at := c.now()
	want := []string{"first", "second", "third", "fourth", "fifth"}
	for _, hash := range want {
		record(t, photos, s.ID, hash, at)
	}

	got, err := photos.BySession(ctx, s.ID)
	if err != nil {
		t.Fatalf("by session: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d frames, want %d", len(got), len(want))
	}
	for i, hash := range want {
		if got[i].ContentHash != hash {
			t.Fatalf("frame %d is %q, want %q — the strip is out of order",
				i, got[i].ContentHash, hash)
		}
	}
}
