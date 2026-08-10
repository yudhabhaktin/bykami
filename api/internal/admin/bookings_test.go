package admin_test

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
)

const operator = "081234567890"

// seedBookingCatalogue writes just enough of the studio for the console to have
// something to show: two resources so the blackout form has a choice to make, one
// package, and a day the studio is open.
func seedBookingCatalogue(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO booking_resources (id, name, google_calendar_id, created_at)
		 VALUES ('photobox', 'Photobox', 'pb@group.calendar.google.com', unixepoch()),
		        ('self-photo', 'Self photo', '', unixepoch())`,
		`INSERT INTO booking_services
		   (id, resource_id, name, service_line, price_idr, duration_minutes, buffer_minutes,
		    headcount_min, headcount_max, created_at)
		 VALUES ('photobox-y2k', 'photobox', 'Y2K', 'photobox', 30000, 10, 20, 1, 5, unixepoch())`,
		`INSERT INTO booking_hours (weekday, opens_at, closes_at)
		 VALUES (0,540,1260),(1,540,1260),(2,540,1260),(3,540,1260),(4,540,1260),(5,540,1260),(6,540,1260)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed booking: %v", err)
		}
	}
}

// tomorrowAt is a bookable instant whatever time the suite runs at.
func tomorrowAt(hour int) time.Time {
	wib := time.FixedZone("WIB", 7*60*60)
	d := time.Now().In(wib).AddDate(0, 0, 1)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, wib)
}

func bookOne(t *testing.T, db *sql.DB, name string, at time.Time) booking.Booking {
	t.Helper()
	desk := booking.New(db, 0)
	b, err := desk.Book(t.Context(), booking.Request{
		ServiceID: "photobox-y2k",
		StartsAt:  at,
		Headcount: 2,
		Name:      name,
		Phone:     "+6281299998888",
	})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	return b
}

func TestBookingDayShowsWhoIsComing(t *testing.T) {
	f := newFixture(t, true, operator)
	seedBookingCatalogue(t, f.db)
	at := tomorrowAt(14)
	bookOne(t, f.db, "Rina Wulandari", at)

	cookie := f.signIn(t, operator)
	day := at.Format("2006-01-02")
	w := f.get(t, "/bookings?day="+day, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("bookings = %d, want 200: %s", w.Code, w.Body)
	}
	body := w.Body.String()

	for _, want := range []string{"Rina Wulandari", "14:00", "Y2K"} {
		if !strings.Contains(body, want) {
			t.Errorf("the day view does not show %q", want)
		}
	}
	// The package's name, not its id. An operator reading "photobox-y2k" has to
	// translate; "Y2K" is what is on the price list and what gets said out loud.
	if strings.Contains(body, "photobox-y2k") {
		t.Error("the day view shows a service id where a name belongs")
	}
	// The times are stored in UTC. A template that printed them without
	// converting would tell an operator this session starts at seven in the
	// morning.
	if strings.Contains(body, "07:00") {
		t.Error("a booking time was rendered in UTC")
	}
}

func TestBookingDayReportsCalendarHealth(t *testing.T) {
	f := newFixture(t, true, operator)
	seedBookingCatalogue(t, f.db)
	desk := booking.New(f.db, 0)

	if err := desk.RecordSyncFailure(t.Context(), "photobox", "notFound — check it is shared"); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	cookie := f.signIn(t, operator)
	body := f.get(t, "/bookings", cookie).Body.String()

	// The whole point of this row: a calendar that stopped syncing is otherwise
	// silent, and the first symptom is two groups at one door.
	if !strings.Contains(body, "notFound") {
		t.Error("a failed calendar sync is not reported to the operator")
	}
	if !strings.Contains(body, "belum terhubung") {
		t.Error("a resource with no calendar is not shown as unconnected")
	}
}

func TestOperatorCanCancelWithoutTheCustomersNumber(t *testing.T) {
	f := newFixture(t, true, operator)
	seedBookingCatalogue(t, f.db)
	at := tomorrowAt(15)
	b := bookOne(t, f.db, "Budi", at)

	cookie := f.signIn(t, operator)
	day := at.Format("2006-01-02")
	csrf := csrfFrom(t, f.get(t, "/bookings?day="+day, cookie).Body.String())

	w := f.post(t, "/bookings/"+b.ID+"/cancel",
		url.Values{"csrf": {csrf}, "day": {day}}, cookie)
	// 303 and never a rendered page, so a refresh cannot resubmit the
	// cancellation.
	if w.Code != http.StatusSeeOther {
		t.Fatalf("cancel = %d, want 303: %s", w.Code, w.Body)
	}

	got, err := booking.New(f.db, 0).Get(t.Context(), b.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", got.Status)
	}

	// And the slot is free again, which is the reason an operator does this at all.
	if _, err := booking.New(f.db, 0).Book(t.Context(), booking.Request{
		ServiceID: "photobox-y2k", StartsAt: at, Headcount: 2,
		Name: "Siti", Phone: "+6281200001111",
	}); err != nil {
		t.Errorf("rebooking the cancelled slot: %v", err)
	}
}

func TestBlockingClosesTheSchedule(t *testing.T) {
	f := newFixture(t, true, operator)
	seedBookingCatalogue(t, f.db)
	at := tomorrowAt(14)

	cookie := f.signIn(t, operator)
	day := at.Format("2006-01-02")
	csrf := csrfFrom(t, f.get(t, "/bookings?day="+day, cookie).Body.String())

	w := f.post(t, "/bookings/block", url.Values{
		"csrf": {csrf}, "day": {day},
		"from": {"14:00"}, "to": {"15:00"},
		"reason": {"Perbaikan lampu"},
	}, cookie)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("block = %d, want 303: %s", w.Code, w.Body)
	}

	if _, err := booking.New(f.db, 0).Book(t.Context(), booking.Request{
		ServiceID: "photobox-y2k", StartsAt: at, Headcount: 2,
		Name: "Siti", Phone: "+6281200001111",
	}); err == nil {
		t.Error("a blocked slot was booked anyway")
	}
	// The half hour after the block is untouched.
	if _, err := booking.New(f.db, 0).Book(t.Context(), booking.Request{
		ServiceID: "photobox-y2k", StartsAt: at.Add(time.Hour), Headcount: 2,
		Name: "Siti", Phone: "+6281200001111",
	}); err != nil {
		t.Errorf("the block reached past its end time: %v", err)
	}
}

func TestBlockingWithNoHoursClosesTheWholeDay(t *testing.T) {
	f := newFixture(t, true, operator)
	seedBookingCatalogue(t, f.db)
	at := tomorrowAt(14)

	cookie := f.signIn(t, operator)
	day := at.Format("2006-01-02")
	csrf := csrfFrom(t, f.get(t, "/bookings?day="+day, cookie).Body.String())

	// The entry an operator reaches for most: closed today, no times typed.
	if w := f.post(t, "/bookings/block", url.Values{
		"csrf": {csrf}, "day": {day}, "reason": {"Libur"},
	}, cookie); w.Code != http.StatusSeeOther {
		t.Fatalf("block = %d, want 303: %s", w.Code, w.Body)
	}

	desk := booking.New(f.db, 0)
	for _, hour := range []int{9, 14, 20} {
		if _, err := desk.Book(t.Context(), booking.Request{
			ServiceID: "photobox-y2k", StartsAt: tomorrowAt(hour), Headcount: 2,
			Name: "Siti", Phone: "+6281200001111",
		}); err == nil {
			t.Errorf("%02d:00 was bookable on a closed day", hour)
		}
	}
}

func TestBlockingRefusesABackwardsRange(t *testing.T) {
	f := newFixture(t, true, operator)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operator)
	day := tomorrowAt(14).Format("2006-01-02")
	csrf := csrfFrom(t, f.get(t, "/bookings?day="+day, cookie).Body.String())

	w := f.post(t, "/bookings/block", url.Values{
		"csrf": {csrf}, "day": {day}, "from": {"15:00"}, "to": {"14:00"},
	}, cookie)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("block = %d, want 303", w.Code)
	}
	// Reported back through the redirect, the same way every other form error in
	// this console is.
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want an error message", loc)
	}
	var blackouts int
	f.db.QueryRow(`SELECT COUNT(*) FROM booking_blackouts`).Scan(&blackouts)
	if blackouts != 0 {
		t.Errorf("%d blackouts written for a backwards range, want 0", blackouts)
	}
}

func TestBookingRoutesNeedAnOperatorAndACSRFToken(t *testing.T) {
	f := newFixture(t, true, operator)
	seedBookingCatalogue(t, f.db)
	at := tomorrowAt(16)
	b := bookOne(t, f.db, "Budi", at)

	// Signed out entirely. staffOnly sends a stranger to the login page rather
	// than rendering the day behind it.
	w := f.get(t, "/bookings", "")
	if w.Code != http.StatusSeeOther {
		t.Errorf("the day view was served to a stranger: %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want the login page", loc)
	}
	if strings.Contains(w.Body.String(), "Budi") {
		t.Error("a customer's name leaked to a signed-out request")
	}

	cookie := f.signIn(t, operator)
	// Signed in, but no token — the same shape as every other mutation here.
	for _, path := range []string{"/bookings/" + b.ID + "/cancel", "/bookings/block"} {
		if w := f.post(t, path, url.Values{"day": {at.Format("2006-01-02")}}, cookie); w.Code != http.StatusForbidden {
			t.Errorf("%s without a CSRF token = %d, want 403", path, w.Code)
		}
	}

	got, _ := booking.New(f.db, 0).Get(t.Context(), b.ID)
	if got.Status != "confirmed" {
		t.Error("a tokenless request cancelled a booking")
	}
}
