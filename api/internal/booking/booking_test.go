package booking

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// The instant every test sits at: Monday 10 August 2026, 19:53 WIB.
//
// Taken from a real reading of the studio's own booking page, which at that
// moment offered exactly one slot for the rest of the day — 20:30. Sitting the
// tests where the observation was made means the notice and closing-time cases
// assert against something that was true rather than something invented.
var testNow = time.Date(2026, 8, 10, 19, 53, 25, 0, WIB)

func newDesk(t *testing.T) (*Desk, *sql.DB) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	seed(t, db)
	d := New(db, 0)
	d.now = func() time.Time { return testNow }
	return d, db
}

// seed writes the studio as it actually trades: three resources, four packages
// spanning one to six slots, 09:00-21:00 every day, and the two prayer breaks.
func seed(t *testing.T, db *sql.DB) {
	t.Helper()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}

	for _, r := range []struct{ id, name string }{
		{"photobox", "Photobox"},
		{"self-photo", "Self photo"},
		{"photographer", "Fotografer"},
	} {
		exec(`INSERT INTO booking_resources (id, name, created_at) VALUES (?, ?, unixepoch())`,
			r.id, r.name)
	}

	for _, s := range []struct {
		id, resource, line string
		price              int64
		perPerson          int
		duration, buffer   int
		min, max           int
	}{
		{"photobox-y2k", "photobox", "photobox", 30_000, 1, 10, 0, 1, 5},
		{"self-mini", "self-photo", "self-photo", 45_000, 0, 15, 0, 1, 2},
		{"pas-kedinasan", "self-photo", "pas-foto", 250_000, 0, 40, 0, 2, 2},
		{"photographer-3h", "photographer", "outdoor-photographer", 850_000, 0, 180, 0, 1, 10},
	} {
		exec(`INSERT INTO booking_services
		        (id, resource_id, name, service_line, price_idr, price_per_person,
		         duration_minutes, buffer_minutes, headcount_min, headcount_max, created_at)
		      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch())`,
			s.id, s.resource, s.id, s.line, s.price, s.perPerson,
			s.duration, s.buffer, s.min, s.max)
	}

	// 09:00-21:00, every day.
	for wd := range 7 {
		exec(`INSERT INTO booking_hours (weekday, opens_at, closes_at) VALUES (?, 540, 1260)`, wd)
	}

	// Maghrib, every day. One row, because it does not move.
	exec(`INSERT INTO booking_breaks (id, weekday, starts_at, ends_at, reason)
	      VALUES ('maghrib', NULL, 1050, 1080, 'Maghrib')`)
	// Dzuhur at 12:00 — except Friday, when Jumatan moves it to 11:30.
	for _, wd := range []int{0, 1, 2, 3, 4, 6} {
		exec(`INSERT INTO booking_breaks (id, weekday, starts_at, ends_at, reason)
		      VALUES (?, ?, 720, 750, 'Dzuhur')`, "dzuhur-"+string(rune('0'+wd)), wd)
	}
	exec(`INSERT INTO booking_breaks (id, weekday, starts_at, ends_at, reason)
	      VALUES ('jumatan', 5, 690, 720, 'Jumatan')`)
}

// wib builds an instant in studio-local time, which is how every expectation in
// these tests is written — nobody reasons about the studio's day in UTC.
func wib(y int, m time.Month, d, hour, min int) time.Time {
	return time.Date(y, m, d, hour, min, 0, 0, WIB)
}

func request(service string, at time.Time, heads int) Request {
	return Request{
		ServiceID: service,
		StartsAt:  at,
		Headcount: heads,
		Name:      "Rina",
		Phone:     "+6281234567890",
		Email:     "rina@example.com",
	}
}

func TestBookingReservesTheSlot(t *testing.T) {
	d, db := newDesk(t)
	ctx := context.Background()

	b, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 3))
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if b.Status != "confirmed" {
		t.Errorf("status = %q, want confirmed", b.Status)
	}
	// The session, not the slot: ten minutes of a thirty-minute reservation.
	if got := b.EndsAt.Sub(b.StartsAt); got != 10*time.Minute {
		t.Errorf("session ran %v, want 10m", got)
	}

	var slots int
	db.QueryRow(`SELECT COUNT(*) FROM booking_slots WHERE booking_id = ?`, b.ID).Scan(&slots)
	if slots != 1 {
		t.Errorf("reserved %d half hours, want 1", slots)
	}
}

func TestConcurrentBookingsOfOneSlotYieldExactlyOne(t *testing.T) {
	d, db := newDesk(t)
	ctx := context.Background()
	at := wib(2026, 8, 12, 14, 0)

	// Sixteen groups tapping the same Saturday-evening slot at once. Every one of
	// them reads availability before any of them writes, so the pre-flight check
	// inside Book passes for all sixteen and the database is the only thing that
	// decides. This is the case a check-then-insert loses.
	const goroutines = 16
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Go(func() {
			_, errs[i] = d.Book(ctx, request("photobox-y2k", at, 2))
		})
	}
	wg.Wait()

	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrSlotTaken):
		default:
			t.Errorf("goroutine %d failed with an unexpected error: %v", i, err)
		}
	}
	if won != 1 {
		t.Errorf("%d bookings succeeded, want exactly 1", won)
	}

	var confirmed int
	db.QueryRow(`SELECT COUNT(*) FROM bookings WHERE status = 'confirmed'`).Scan(&confirmed)
	if confirmed != 1 {
		t.Errorf("%d confirmed bookings written, want 1 — two groups hold the same room", confirmed)
	}
}

func TestSchemaRefusesTwoBookingsInOneSlot(t *testing.T) {
	d, db := newDesk(t)
	ctx := context.Background()

	b, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2))
	if err != nil {
		t.Fatalf("book: %v", err)
	}

	// Bypasses this package entirely. The guarantee has to hold against a direct
	// write, or it is a convention rather than a constraint — and whoever adds the
	// next caller in two years will not have read this file.
	_, err = db.Exec(
		`INSERT INTO booking_slots (resource_id, starts_at, booking_id) VALUES (?, ?, ?)`,
		b.ResourceID, b.StartsAt.Unix(), b.ID)
	if err == nil {
		t.Error("schema accepted a second reservation of one half hour")
	}
}

func TestOverlappingLongSessionsCannotBothBeBooked(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	// A three-hour shoot from 09:00, then a second one from 10:00. Their start
	// times differ, so a unique index on the start instant would accept both and
	// send one photographer to two places. The reservation is per half hour, so
	// the second collides on 10:00.
	if _, err := d.Book(ctx, request("photographer-3h", wib(2026, 8, 12, 9, 0), 4)); err != nil {
		t.Fatalf("first shoot: %v", err)
	}
	_, err := d.Book(ctx, request("photographer-3h", wib(2026, 8, 12, 10, 0), 4))
	if !errors.Is(err, ErrSlotTaken) {
		t.Errorf("second shoot returned %v, want ErrSlotTaken", err)
	}
}

func TestALongSessionReservesEveryHalfHourItRuns(t *testing.T) {
	d, db := newDesk(t)
	ctx := context.Background()

	b, err := d.Book(ctx, request("photographer-3h", wib(2026, 8, 12, 9, 0), 4))
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	var slots int
	db.QueryRow(`SELECT COUNT(*) FROM booking_slots WHERE booking_id = ?`, b.ID).Scan(&slots)
	if slots != 6 {
		t.Errorf("a three-hour session reserved %d half hours, want 6", slots)
	}

	// A 40-minute session is not a whole number of slots, and must take the slot
	// it spills into rather than letting somebody book on top of its last ten
	// minutes.
	k, err := d.Book(ctx, request("pas-kedinasan", wib(2026, 8, 12, 15, 0), 2))
	if err != nil {
		t.Fatalf("kedinasan: %v", err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM booking_slots WHERE booking_id = ?`, k.ID).Scan(&slots)
	if slots != 2 {
		t.Errorf("a forty-minute session reserved %d half hours, want 2", slots)
	}
	if _, err := d.Book(ctx, request("self-mini", wib(2026, 8, 12, 15, 30), 2)); !errors.Is(err, ErrSlotTaken) {
		t.Errorf("booking over the spill returned %v, want ErrSlotTaken", err)
	}
}

func TestDifferentResourcesRunInParallel(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()
	at := wib(2026, 8, 12, 14, 0)

	if _, err := d.Book(ctx, request("photobox-y2k", at, 2)); err != nil {
		t.Fatalf("photobox: %v", err)
	}
	// The two YouCanBook.me pages served one shared pool, so this second booking
	// was impossible until now. It is the capacity this replacement adds.
	if _, err := d.Book(ctx, request("self-mini", at, 2)); err != nil {
		t.Errorf("self photo at the same time as the photobox: %v", err)
	}
}

func TestCancellingFreesTheSlot(t *testing.T) {
	d, db := newDesk(t)
	ctx := context.Background()
	at := wib(2026, 8, 12, 14, 0)

	b, err := d.Book(ctx, request("photobox-y2k", at, 2))
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if _, err := d.Cancel(ctx, b.ID, "+6281234567890"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := d.Book(ctx, request("photobox-y2k", at, 4)); err != nil {
		t.Errorf("rebooking a cancelled slot: %v", err)
	}

	// The cancelled booking is still on the record. A slot that was taken and
	// given back is a question somebody asks at the end of the month.
	var kept int
	db.QueryRow(`SELECT COUNT(*) FROM bookings WHERE status = 'cancelled'`).Scan(&kept)
	if kept != 1 {
		t.Errorf("%d cancelled bookings kept, want 1", kept)
	}
}

func TestCancellingIsIdempotent(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	b, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2))
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if _, err := d.Cancel(ctx, b.ID, "+6281234567890"); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	// A customer double-tapping the cancel link must not see an error.
	if _, err := d.Cancel(ctx, b.ID, "+6281234567890"); err != nil {
		t.Errorf("second cancel: %v", err)
	}
}

func TestCancellingNeedsTheNumberThatBooked(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	b, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2))
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	// Reads as "no such booking" rather than "wrong number", so a guessed id
	// cannot be used to establish that the studio has a customer at that time.
	if _, err := d.Cancel(ctx, b.ID, "+6289999999999"); !errors.Is(err, ErrNoBooking) {
		t.Errorf("cancel with the wrong number returned %v, want ErrNoBooking", err)
	}
	// The operator path passes no number, because the console already knows who
	// is asking.
	if _, err := d.Cancel(ctx, b.ID, ""); err != nil {
		t.Errorf("operator cancel: %v", err)
	}
}

func TestBookingRefusesWhatTheStudioDoesNotSell(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	tests := []struct {
		name string
		req  Request
		want error
	}{
		{
			"a group too big for the package",
			request("self-mini", wib(2026, 8, 12, 14, 0), 5),
			ErrHeadcount,
		},
		{
			"a group too small for the package",
			request("pas-kedinasan", wib(2026, 8, 12, 14, 0), 1),
			ErrHeadcount,
		},
		{
			"a time that is not on the half hour",
			request("photobox-y2k", wib(2026, 8, 12, 14, 10), 2),
			ErrNotOnTheGrid,
		},
		{
			"a time inside the minimum notice",
			request("photobox-y2k", wib(2026, 8, 10, 20, 0), 2),
			ErrTooSoon,
		},
		{
			"a time in the past",
			request("photobox-y2k", wib(2026, 8, 9, 14, 0), 2),
			ErrTooSoon,
		},
		{
			"a time past the end of the window",
			request("photobox-y2k", wib(2026, 11, 1, 14, 0), 2),
			ErrTooFar,
		},
		{
			"before the studio opens",
			request("photobox-y2k", wib(2026, 8, 12, 8, 30), 2),
			ErrNotBookable,
		},
		{
			"a session that would run past closing",
			request("photographer-3h", wib(2026, 8, 12, 19, 0), 2),
			ErrNotBookable,
		},
		{
			"the Maghrib break",
			request("photobox-y2k", wib(2026, 8, 12, 17, 30), 2),
			ErrSlotTaken,
		},
		{
			"the Dzuhur break on a Wednesday",
			request("photobox-y2k", wib(2026, 8, 12, 12, 0), 2),
			ErrSlotTaken,
		},
		{
			"the Jumatan break on a Friday",
			request("photobox-y2k", wib(2026, 8, 14, 11, 30), 2),
			ErrSlotTaken,
		},
		{
			"a service that does not exist",
			request("massage", wib(2026, 8, 12, 14, 0), 2),
			ErrNoService,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.Book(ctx, tc.req); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestFridayKeepsTheMiddayThatOtherDaysLose(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	// The mirror image of the break test: on Friday the midday break has moved to
	// 11:30, so 12:00 is for sale. A single break rule with a Friday exception
	// gets this backwards, which is a customer turned away from a slot the studio
	// would have sold.
	if _, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 14, 12, 0), 2)); err != nil {
		t.Errorf("noon on a Friday: %v", err)
	}
	if _, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 11, 30), 2)); err != nil {
		t.Errorf("half eleven on a Wednesday: %v", err)
	}
}

func TestBlackoutClosesTheStudio(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	from := wib(2026, 8, 20, 0, 0)
	// A NULL resource closes everything, which is what "closed today" means.
	if err := d.Blackout(ctx, "", from, from.AddDate(0, 0, 1), "Idulfitri"); err != nil {
		t.Fatalf("blackout: %v", err)
	}
	if _, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 20, 14, 0), 2)); !errors.Is(err, ErrSlotTaken) {
		t.Errorf("booking a blacked-out day returned %v, want ErrSlotTaken", err)
	}
	if _, err := d.Book(ctx, request("self-mini", wib(2026, 8, 20, 14, 0), 2)); !errors.Is(err, ErrSlotTaken) {
		t.Errorf("a blackout on every resource missed self photo: %v", err)
	}
	// The day after is unaffected.
	if _, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 21, 14, 0), 2)); err != nil {
		t.Errorf("the day after a blackout: %v", err)
	}
}

func TestUpcomingAnswersWhetherAnybodyHasBooked(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	// The state this exists for: a box where the console cannot be signed into, no
	// calendar is connected and no WhatsApp provider exists — so a booking nobody
	// can see is a booking nobody turns up for.
	if got, err := d.Upcoming(ctx, 10); err != nil || len(got) != 0 {
		t.Fatalf("an empty studio reported %d bookings (%v), want 0", len(got), err)
	}

	later, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 20, 14, 0), 2))
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	sooner, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 9, 0), 2))
	if err != nil {
		t.Fatalf("book: %v", err)
	}

	got, err := d.Upcoming(ctx, 10)
	if err != nil {
		t.Fatalf("upcoming: %v", err)
	}
	// Soonest first: the operator reads the top of the list to find out who is
	// coming next, not who booked first.
	if len(got) != 2 || got[0].ID != sooner.ID || got[1].ID != later.ID {
		t.Fatalf("upcoming returned %d bookings in the wrong order", len(got))
	}

	// A cancelled booking is not upcoming. It stays in Day, which is history.
	if _, err := d.Cancel(ctx, sooner.ID, ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, _ = d.Upcoming(ctx, 10)
	if len(got) != 1 || got[0].ID != later.ID {
		t.Errorf("a cancelled booking is still listed as upcoming: %+v", got)
	}

	// Neither is one that has already happened. The suite's clock sits on
	// 10 August, so a booking on the 12th is ahead and one in July is not.
	if _, err := d.db.Exec(
		`INSERT INTO bookings (id, resource_id, service_id, starts_at, ends_at, headcount,
		                       name, phone, status, created_at)
		 VALUES ('past', 'photobox', 'photobox-y2k', ?, ?, 2, 'Lama', '+6281200000000',
		         'confirmed', unixepoch())`,
		wib(2026, 7, 1, 14, 0).Unix(), wib(2026, 7, 1, 14, 10).Unix()); err != nil {
		t.Fatalf("seed a past booking: %v", err)
	}
	got, _ = d.Upcoming(ctx, 10)
	for _, b := range got {
		if b.ID == "past" {
			t.Error("a booking from July is listed as upcoming")
		}
	}

	// And the limit is honoured, because this is read on a box with one core.
	if got, _ = d.Upcoming(ctx, 1); len(got) != 1 {
		t.Errorf("limit 1 returned %d rows", len(got))
	}
}

func TestGetAndDayReadWhatWasWritten(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	b, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 3))
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	got, err := d.Get(ctx, b.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Rina" || got.Headcount != 3 || !got.StartsAt.Equal(b.StartsAt) {
		t.Errorf("read back %+v, want the booking that was written", got)
	}
	if _, err := d.Get(ctx, "nope"); !errors.Is(err, ErrNoBooking) {
		t.Errorf("get of an unknown id returned %v, want ErrNoBooking", err)
	}

	day, err := d.Day(ctx, wib(2026, 8, 12, 0, 0))
	if err != nil {
		t.Fatalf("day: %v", err)
	}
	if len(day) != 1 {
		t.Fatalf("day listed %d bookings, want 1", len(day))
	}

	// A cancelled booking stays in the day view: an operator looking at a quiet
	// afternoon needs to see that it was busy and emptied.
	if _, err := d.Cancel(ctx, b.ID, ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	day, _ = d.Day(ctx, wib(2026, 8, 12, 0, 0))
	if len(day) != 1 || day[0].Status != "cancelled" {
		t.Errorf("day view lost the cancelled booking: %+v", day)
	}
}
