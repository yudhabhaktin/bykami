package admin_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
)

// fakeCalendar stands in for Google. It also implements booking.Principal, which
// is what lets the settings page print the address calendars must be shared with.
type fakeCalendar struct {
	mu     sync.Mutex
	calls  int
	failOn string
}

func (f *fakeCalendar) ServiceAccount() string {
	return "booking@bykami.iam.gserviceaccount.com"
}

func (f *fakeCalendar) FreeBusy(_ context.Context, calendarID string, _, _ time.Time) ([]booking.Busy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failOn != "" && calendarID == f.failOn {
		// The wording Google actually returns for a calendar that was never
		// shared, which is the failure this page exists to make legible.
		return nil, errNotShared
	}
	return nil, nil
}

func (f *fakeCalendar) Insert(context.Context, string, booking.Event) (string, error) {
	return "evt_1", nil
}
func (f *fakeCalendar) Delete(context.Context, string, string) error { return nil }

func (f *fakeCalendar) freeBusyCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type notSharedError struct{}

func (notSharedError) Error() string {
	return `calendar "x@group.calendar.google.com": notFound — check it is shared`
}

var errNotShared = notSharedError{}

func TestSettingsShowsTheAddressToShareCalendarsWith(t *testing.T) {
	cal := &fakeCalendar{}
	f := newFixtureCal(t, cal, true, operator)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operator)
	body := f.get(t, "/settings", cookie).Body.String()

	// The one step of the setup that happens inside Google and cannot be done from
	// here. Without it printed on the page, an operator would have to read a JSON
	// file on the server to find out what to type.
	if !strings.Contains(body, "booking@bykami.iam.gserviceaccount.com") {
		t.Error("the settings page does not print the service-account address")
	}
	if !strings.Contains(body, "Make changes to events") {
		t.Error("the page does not say which permission to grant")
	}
	// Every resource is listed, connected or not.
	for _, want := range []string{"photobox", "self-photo", "belum terhubung"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not mention %q", want)
		}
	}
}

func TestSettingsSaysWhenThereIsNoCredential(t *testing.T) {
	// The deployed state today, and a different failure from an unshared calendar:
	// there is nothing to share the calendar *with* yet.
	f := newFixtureCal(t, nil, true, operator)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operator)
	body := f.get(t, "/settings", cookie).Body.String()

	if !strings.Contains(body, "BYKAMI_GOOGLE_CREDENTIALS") {
		t.Error("the page does not name the variable that is missing")
	}
	// And it says booking still works, because it does — that is the whole point
	// of the busy ranges being a cache.
	if !strings.Contains(body, "Booking tetap berjalan") {
		t.Error("the page does not reassure that booking still works without Google")
	}
	// No sync button, because there is nothing to sync with.
	if strings.Contains(body, "Tes sinkron sekarang") {
		t.Error("a sync button was offered with no credential configured")
	}
}

func TestOperatorCanConnectACalendar(t *testing.T) {
	cal := &fakeCalendar{}
	f := newFixtureCal(t, cal, true, operator)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operator)
	csrf := csrfFrom(t, f.get(t, "/settings", cookie).Body.String())

	w := f.post(t, "/settings/calendar/self-photo",
		url.Values{"csrf": {csrf}, "calendar_id": {"self@group.calendar.google.com"}}, cookie)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("save = %d, want 303: %s", w.Code, w.Body)
	}

	var got string
	if err := f.db.QueryRow(
		`SELECT google_calendar_id FROM booking_resources WHERE id = 'self-photo'`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "self@group.calendar.google.com" {
		t.Errorf("stored %q", got)
	}

	// And it now shows on the page.
	if !strings.Contains(f.get(t, "/settings", cookie).Body.String(), "self@group.calendar.google.com") {
		t.Error("the saved calendar id is not shown back")
	}
}

func TestConnectingACalendarRefusesSomethingThatIsNotOne(t *testing.T) {
	cal := &fakeCalendar{}
	f := newFixtureCal(t, cal, true, operator)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operator)
	csrf := csrfFrom(t, f.get(t, "/settings", cookie).Body.String())

	// The mistake somebody actually makes: pasting the calendar's name, or the URL
	// out of the browser bar. Caught here, because the symptom otherwise is a sync
	// that fails quietly for a week.
	w := f.post(t, "/settings/calendar/self-photo",
		url.Values{"csrf": {csrf}, "calendar_id": {"Kalender Studio"}}, cookie)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("save = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Fatalf("Location = %q, want an error", loc)
	}
	// The real message, not a generic one — it has to say what a calendar id looks
	// like, or the operator tries the same thing again.
	if !strings.Contains(loc, "group.calendar.google.com") {
		t.Errorf("the error does not show the expected shape: %q", loc)
	}

	var got string
	f.db.QueryRow(`SELECT google_calendar_id FROM booking_resources WHERE id = 'self-photo'`).Scan(&got)
	if got != "" {
		t.Errorf("stored %q for a value that is not a calendar id", got)
	}
}

func TestDetachingACalendarClearsWhatItCached(t *testing.T) {
	cal := &fakeCalendar{}
	f := newFixtureCal(t, cal, true, operator)
	seedBookingCatalogue(t, f.db)
	desk := booking.New(f.db, 0)

	at := tomorrowAt(14)
	if err := desk.ReplaceBusy(t.Context(), "photobox", at, at.Add(2*time.Hour),
		[]booking.Busy{{StartsAt: at, EndsAt: at.Add(time.Hour)}}); err != nil {
		t.Fatalf("replace busy: %v", err)
	}
	if _, err := desk.Book(t.Context(), booking.Request{
		ServiceID: "photobox-y2k", StartsAt: at, Headcount: 2,
		Name: "Siti", Phone: "+6281200001111",
	}); err == nil {
		t.Fatal("the seeded busy range did not block the slot")
	}

	cookie := f.signIn(t, operator)
	csrf := csrfFrom(t, f.get(t, "/settings", cookie).Body.String())
	if w := f.post(t, "/settings/calendar/photobox",
		url.Values{"csrf": {csrf}, "calendar_id": {""}}, cookie); w.Code != http.StatusSeeOther {
		t.Fatalf("detach = %d, want 303", w.Code)
	}

	// The cached ranges belonged to a calendar nobody is reading any more. Left in
	// place they would keep the studio closed against a calendar it no longer
	// consults, which is availability lost for no reason.
	if _, err := desk.Book(t.Context(), booking.Request{
		ServiceID: "photobox-y2k", StartsAt: at, Headcount: 2,
		Name: "Siti", Phone: "+6281200001111",
	}); err != nil {
		t.Errorf("detaching the calendar left its cached busy ranges behind: %v", err)
	}
}

func TestSyncNowReportsWhatGoogleSaid(t *testing.T) {
	cal := &fakeCalendar{failOn: "broken@group.calendar.google.com"}
	f := newFixtureCal(t, cal, true, operator)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operator)
	csrf := csrfFrom(t, f.get(t, "/settings", cookie).Body.String())

	// One calendar shared, one not — the ordinary halfway state of a first setup.
	f.post(t, "/settings/calendar/self-photo",
		url.Values{"csrf": {csrf}, "calendar_id": {"good@group.calendar.google.com"}}, cookie)
	f.post(t, "/settings/calendar/photobox",
		url.Values{"csrf": {csrf}, "calendar_id": {"broken@group.calendar.google.com"}}, cookie)

	if w := f.post(t, "/settings/sync", url.Values{"csrf": {csrf}}, cookie); w.Code != http.StatusSeeOther {
		t.Fatalf("sync = %d, want 303: %s", w.Code, w.Body)
	}
	if cal.freeBusyCalls() < 2 {
		t.Errorf("the button made %d freeBusy calls, want one per connected calendar", cal.freeBusyCalls())
	}

	body := f.get(t, "/settings", cookie).Body.String()
	// The whole reason the button exists: the operator finds out now, in words
	// Google chose, instead of in five minutes from a journal.
	if !strings.Contains(body, "notFound") {
		t.Error("the failing calendar's reason is not shown on the page")
	}
	if !strings.Contains(body, "aktif") {
		t.Error("the working calendar is not shown as active")
	}
}

func TestSyncNowNeedsACredential(t *testing.T) {
	f := newFixtureCal(t, nil, true, operator)
	seedBookingCatalogue(t, f.db)

	cookie := f.signIn(t, operator)
	csrf := csrfFrom(t, f.get(t, "/settings", cookie).Body.String())

	// The button is not rendered, but the route must not panic on a nil worker if
	// somebody posts to it anyway.
	w := f.post(t, "/settings/sync", url.Values{"csrf": {csrf}}, cookie)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("sync with no credential = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want an error", loc)
	}
}

func TestSettingsRoutesNeedAnOperatorAndACSRFToken(t *testing.T) {
	cal := &fakeCalendar{}
	f := newFixtureCal(t, cal, true, operator)
	seedBookingCatalogue(t, f.db)

	if w := f.get(t, "/settings", ""); w.Code != http.StatusSeeOther {
		t.Errorf("settings served to a stranger: %d", w.Code)
	}

	cookie := f.signIn(t, operator)
	for _, path := range []string{"/settings/calendar/photobox", "/settings/sync"} {
		if w := f.post(t, path, url.Values{"calendar_id": {"x@group.calendar.google.com"}}, cookie); w.Code != http.StatusForbidden {
			t.Errorf("%s without a CSRF token = %d, want 403", path, w.Code)
		}
	}

	// Unchanged, not empty: the fixture seeds photobox with a calendar already, and
	// asserting emptiness here would have passed for the wrong reason.
	var got string
	f.db.QueryRow(`SELECT google_calendar_id FROM booking_resources WHERE id = 'photobox'`).Scan(&got)
	if got != "pb@group.calendar.google.com" {
		t.Errorf("a tokenless request changed a calendar to %q", got)
	}
}
