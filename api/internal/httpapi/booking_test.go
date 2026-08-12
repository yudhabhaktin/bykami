package httpapi_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
)

// The studio's own site, and the origin every booking request in these tests
// comes from. It is a different host from the API, which is the whole reason the
// booking routes carry CORS at all.
const studioOrigin = "https://studio.bykami.id"

// seedBooking writes the smallest catalogue a booking needs: one resource, one
// package, hours, and the Maghrib break that every day of the week has.
func seedBooking(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO booking_resources (id, name, created_at)
		 VALUES ('photobox', 'Photobox', unixepoch())`,
		`INSERT INTO booking_services
		   (id, resource_id, name, service_line, description, price_idr, price_per_person,
		    duration_minutes, buffer_minutes, headcount_min, headcount_max, order_index, created_at)
		 VALUES ('photobox-y2k', 'photobox', 'Y2K', 'photobox', '2 strip print 2R tiap orang',
		         30000, 1, 10, 20, 1, 5, 0, unixepoch())`,
		`INSERT INTO booking_hours (weekday, opens_at, closes_at)
		 VALUES (0,540,1260),(1,540,1260),(2,540,1260),(3,540,1260),(4,540,1260),(5,540,1260),(6,540,1260)`,
		`INSERT INTO booking_breaks (id, weekday, starts_at, ends_at, reason)
		 VALUES ('maghrib', NULL, 1050, 1080, 'Maghrib')`,

		// A second package, on its own resource so it never races the photobox
		// above, and the only one here that asks which wall to hang. The photobox
		// keeps offering nothing, which is what makes "backdrops": [] on that row
		// an assertion rather than a coincidence.
		`INSERT INTO booking_resources (id, name, created_at)
		 VALUES ('self-photo', 'Self photo', unixepoch())`,
		`INSERT INTO booking_services
		   (id, resource_id, name, service_line, description, price_idr, price_per_person,
		    duration_minutes, buffer_minutes, headcount_min, headcount_max, order_index, created_at)
		 VALUES ('self-midi', 'self-photo', 'MIDI', 'self-photo', '2 print 4R',
		         70000, 0, 20, 10, 1, 4, 1, unixepoch())`,
		`INSERT INTO booking_backdrops (id, name, created_at)
		 VALUES ('white', 'White', unixepoch()), ('ivory', 'Ivory', unixepoch())`,
		// White first, against the alphabet, so the response proves it carries the
		// package's own order and not the database's.
		`INSERT INTO booking_service_backdrops (service_id, backdrop_id, order_index)
		 VALUES ('self-midi', 'white', 0), ('self-midi', 'ivory', 1)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed booking: %v", err)
		}
	}
}

// bookingRequest posts as a browser on the studio site does, with an Origin.
func bookingRequest(t *testing.T, h http.Handler, method, path, origin, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// nextSlot is a time that is definitely bookable: tomorrow at 14:00 WIB, which
// clears the minimum notice and both prayer breaks whatever day the suite runs on.
func nextSlot() time.Time {
	now := time.Now().In(booking.WIB).AddDate(0, 0, 1)
	return time.Date(now.Year(), now.Month(), now.Day(), 14, 0, 0, 0, booking.WIB)
}

func TestBookingCatalogueIsPublicAndCacheable(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	w := bookingRequest(t, h, http.MethodGet, "/v1/booking/services", studioOrigin, "")
	if w.Code != http.StatusOK {
		t.Fatalf("services = %d, want 200: %s", w.Code, w.Body)
	}

	out := decodeBody[struct {
		Services []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			PriceIDR       int64  `json:"price_idr"`
			PricePerPerson bool   `json:"price_per_person"`
			Backdrops      []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"backdrops"`
		} `json:"services"`
	}](t, w)
	if len(out.Services) != 2 || out.Services[0].ID != "photobox-y2k" {
		t.Fatalf("catalogue = %+v", out.Services)
	}
	if out.Services[0].PriceIDR != 30000 || !out.Services[0].PricePerPerson {
		t.Errorf("price = %d per-person %v, want 30000 per person",
			out.Services[0].PriceIDR, out.Services[0].PricePerPerson)
	}

	// The walls the page renders, in the package's own order. A booth has none,
	// and the page has to be able to tell that from a response that predates
	// backdrops — which is why the field is never omitted.
	if n := len(out.Services[0].Backdrops); n != 0 {
		t.Errorf("the photobox offers %d backdrops, want none", n)
	}
	walls := out.Services[1].Backdrops
	if len(walls) != 2 || walls[0].ID != "white" || walls[1].Name != "Ivory" {
		t.Errorf("self-midi backdrops = %+v, want white then Ivory", walls)
	}
	if !strings.Contains(w.Body.String(), `"backdrops":[]`) {
		t.Error("a package with no backdrops sent null or nothing, not an empty list")
	}

	// A price list is not personal data, and this is read by every visitor to the
	// booking page. write() defaults to no-store, so this asserts the handler's
	// override actually survives it — it did not, until write stopped overwriting
	// a Cache-Control that was already set.
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("Cache-Control = %q, want public, max-age=300", got)
	}
}

func TestAvailabilityIsServedInWIB(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	day := nextSlot().Format("2006-01-02")
	w := bookingRequest(t, h, http.MethodGet,
		"/v1/booking/availability?service=photobox-y2k&from="+day+"&to="+
			nextSlot().AddDate(0, 0, 1).Format("2006-01-02"), studioOrigin, "")
	if w.Code != http.StatusOK {
		t.Fatalf("availability = %d, want 200: %s", w.Code, w.Body)
	}

	out := decodeBody[struct {
		Slots []struct {
			StartsAt string `json:"starts_at"`
		} `json:"slots"`
	}](t, w)
	if len(out.Slots) == 0 {
		t.Fatal("a whole day offered no slots")
	}
	// +07:00, not Z. The client renders these directly and the studio's day is a
	// local fact; handing a browser UTC invites it to convert and show the wrong
	// hour to somebody standing in Banyuwangi.
	parsed, err := time.Parse(time.RFC3339, out.Slots[0].StartsAt)
	if err != nil {
		t.Fatalf("slot %q is not RFC3339: %v", out.Slots[0].StartsAt, err)
	}
	if _, offset := parsed.Zone(); offset != 7*3600 {
		t.Errorf("slot %q carries offset %ds, want +07:00", out.Slots[0].StartsAt, offset)
	}
}

func TestAvailabilityRefusesNonsenseRanges(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"no service", "?from=2026-08-12&to=2026-08-13", http.StatusBadRequest},
		{"no dates", "?service=photobox-y2k", http.StatusBadRequest},
		{"unparseable date", "?service=photobox-y2k&from=tomorrow&to=2026-08-13", http.StatusBadRequest},
		{"backwards range", "?service=photobox-y2k&from=2026-08-13&to=2026-08-12", http.StatusBadRequest},
		// An unbounded range is a way to make a 2 vCPU box walk a year of half
		// hours for one unauthenticated request.
		{"a year", "?service=photobox-y2k&from=2026-08-12&to=2027-08-12", http.StatusBadRequest},
		{"unknown service", "?service=massage&from=2026-08-12&to=2026-08-13", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := bookingRequest(t, h, http.MethodGet, "/v1/booking/availability"+tc.query, studioOrigin, "")
			if w.Code != tc.want {
				t.Errorf("got %d, want %d: %s", w.Code, tc.want, w.Body)
			}
		})
	}
}

func TestBookingWithoutALoginCreatesTheAccount(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	body := `{"service":"photobox-y2k","starts_at":"` + nextSlot().Format(time.RFC3339) +
		`","headcount":3,"name":"Rina","phone":"081234567890","email":"rina@example.com","terms":true}`
	w := bookingRequest(t, h, http.MethodPost, "/v1/booking", studioOrigin, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("booking = %d, want 201: %s", w.Code, w.Body)
	}

	out := decodeBody[struct {
		ID     string `json:"id"`
		Phone  string `json:"phone"`
		Status string `json:"status"`
	}](t, w)
	if out.Status != "confirmed" {
		t.Errorf("status = %q, want confirmed", out.Status)
	}
	// Normalised on the way in. The number is the account, so two spellings of one
	// number would be two customers and two loyalty balances.
	if out.Phone != "+6281234567890" {
		t.Errorf("phone = %q, want E.164", out.Phone)
	}

	// The account exists, so loyalty has something to attach to when it launches —
	// which is what the architecture record requires of every vertical, and the
	// reason booking does not keep its own customer table.
	var name, email string
	if err := db.QueryRow(
		`SELECT COALESCE(name,''), COALESCE(email,'') FROM users WHERE phone = ?`,
		"+6281234567890").Scan(&name, &email); err != nil {
		t.Fatalf("the booking created no account: %v", err)
	}
	if name != "Rina" || email != "rina@example.com" {
		t.Errorf("account = %q/%q, want the details from the booking", name, email)
	}

	// And it is readable by its id, which is what the confirmation page does.
	back := bookingRequest(t, h, http.MethodGet, "/v1/booking/"+out.ID, studioOrigin, "")
	if back.Code != http.StatusOK {
		t.Errorf("read back = %d, want 200: %s", back.Code, back.Body)
	}
	// Personal data. no-store even though availability beside it is cached.
	if got := back.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control on a booking = %q, want no-store", got)
	}
}

// The wall travels the whole way: chosen on the page, checked here, and echoed
// back by name so the confirmation screen can print what the studio will hang.
func TestBookingCarriesTheChosenBackdrop(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	at := nextSlot().Format(time.RFC3339)
	body := `{"service":"self-midi","starts_at":"` + at +
		`","headcount":3,"name":"Rina","phone":"081234567890","backdrop":"ivory","terms":true}`
	w := bookingRequest(t, h, http.MethodPost, "/v1/booking", studioOrigin, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("booking = %d, want 201: %s", w.Code, w.Body)
	}

	out := decodeBody[struct {
		ID       string `json:"id"`
		Backdrop string `json:"backdrop"`
	}](t, w)
	// The name, not the id: this is what a customer reads on the receipt.
	if out.Backdrop != "Ivory" {
		t.Errorf("backdrop = %q, want Ivory", out.Backdrop)
	}

	// And it survives the read the confirmation page makes, which goes through the
	// join rather than through anything the request carried.
	back := bookingRequest(t, h, http.MethodGet, "/v1/booking/"+out.ID, studioOrigin, "")
	if got := decodeBody[struct {
		Backdrop string `json:"backdrop"`
	}](t, back).Backdrop; got != "Ivory" {
		t.Errorf("read back %q, want Ivory", got)
	}
}

// 400 and not 409 on both: unlike a taken slot, neither of these became wrong
// while the customer was filling the form in.
func TestBookingRefusesAWallThePackageDoesNotOffer(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	at := nextSlot().Format(time.RFC3339)
	tests := []struct {
		name string
		body string
	}{
		{
			"no background chosen",
			`{"service":"self-midi","starts_at":"` + at + `","headcount":3,"name":"Rina","phone":"081234567890","terms":true}`,
		},
		{
			"a colour the studio does not stock",
			`{"service":"self-midi","starts_at":"` + at + `","headcount":3,"name":"Rina","phone":"081234567890","backdrop":"chartreuse","terms":true}`,
		},
		{
			"a background named for a booth that has one built in",
			`{"service":"photobox-y2k","starts_at":"` + at + `","headcount":2,"name":"Rina","phone":"081234567890","backdrop":"ivory","terms":true}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := bookingRequest(t, h, http.MethodPost, "/v1/booking", studioOrigin, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("got %d, want 400: %s", w.Code, w.Body)
			}
		})
	}

	var booked int
	db.QueryRow(`SELECT COUNT(*) FROM bookings`).Scan(&booked)
	if booked != 0 {
		t.Errorf("%d bookings written by requests that were refused", booked)
	}
}

func TestBookingRefusesWhatTheStudioCannotHonour(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	at := nextSlot().Format(time.RFC3339)
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			"no terms accepted",
			`{"service":"photobox-y2k","starts_at":"` + at + `","headcount":2,"name":"Rina","phone":"081234567890"}`,
			http.StatusBadRequest,
		},
		{
			"no name",
			`{"service":"photobox-y2k","starts_at":"` + at + `","headcount":2,"name":"  ","phone":"081234567890","terms":true}`,
			http.StatusBadRequest,
		},
		{
			"not a mobile number",
			`{"service":"photobox-y2k","starts_at":"` + at + `","headcount":2,"name":"Rina","phone":"12345","terms":true}`,
			http.StatusBadRequest,
		},
		{
			"too many people for the package",
			`{"service":"photobox-y2k","starts_at":"` + at + `","headcount":9,"name":"Rina","phone":"081234567890","terms":true}`,
			http.StatusBadRequest,
		},
		{
			"a time in the past",
			`{"service":"photobox-y2k","starts_at":"2020-01-01T14:00:00+07:00","headcount":2,"name":"Rina","phone":"081234567890","terms":true}`,
			http.StatusConflict,
		},
		{
			"not on the half hour",
			`{"service":"photobox-y2k","starts_at":"` + nextSlot().Add(7*time.Minute).Format(time.RFC3339) + `","headcount":2,"name":"Rina","phone":"081234567890","terms":true}`,
			http.StatusBadRequest,
		},
		{
			"a package that does not exist",
			`{"service":"massage","starts_at":"` + at + `","headcount":2,"name":"Rina","phone":"081234567890","terms":true}`,
			http.StatusNotFound,
		},
		{
			"a misspelled field",
			`{"service":"photobox-y2k","starts_at":"` + at + `","head_count":2,"name":"Rina","phone":"081234567890","terms":true}`,
			http.StatusBadRequest,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := bookingRequest(t, h, http.MethodPost, "/v1/booking", studioOrigin, tc.body)
			if w.Code != tc.want {
				t.Errorf("got %d, want %d: %s", w.Code, tc.want, w.Body)
			}
		})
	}
}

func TestTheSecondBookingOfASlotIsAConflict(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	body := `{"service":"photobox-y2k","starts_at":"` + nextSlot().Format(time.RFC3339) +
		`","headcount":2,"name":"Rina","phone":"081234567890","terms":true}`
	if w := bookingRequest(t, h, http.MethodPost, "/v1/booking", studioOrigin, body); w.Code != http.StatusCreated {
		t.Fatalf("first booking = %d: %s", w.Code, w.Body)
	}

	// 409 and not 400: the request was well formed and would have worked a moment
	// earlier, so the client should offer another time rather than correct a field.
	w := bookingRequest(t, h, http.MethodPost, "/v1/booking", studioOrigin, body)
	if w.Code != http.StatusConflict {
		t.Errorf("second booking = %d, want 409: %s", w.Code, w.Body)
	}
}

func TestCancellingNeedsTheNumberThatBooked(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	body := `{"service":"photobox-y2k","starts_at":"` + nextSlot().Format(time.RFC3339) +
		`","headcount":2,"name":"Rina","phone":"081234567890","terms":true}`
	created := bookingRequest(t, h, http.MethodPost, "/v1/booking", studioOrigin, body)
	id := decodeBody[struct {
		ID string `json:"id"`
	}](t, created).ID

	// Knowing the id is not enough. It is weak proof and it is the proof the form
	// collected — the alternative is a customer who cannot cancel at all.
	wrong := bookingRequest(t, h, http.MethodPost, "/v1/booking/"+id+"/cancel", studioOrigin,
		`{"phone":"081999999999"}`)
	if wrong.Code != http.StatusNotFound {
		t.Errorf("cancel with the wrong number = %d, want 404", wrong.Code)
	}

	right := bookingRequest(t, h, http.MethodPost, "/v1/booking/"+id+"/cancel", studioOrigin,
		`{"phone":"081234567890"}`)
	if right.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200: %s", right.Code, right.Body)
	}
	if got := decodeBody[struct {
		Status string `json:"status"`
	}](t, right).Status; got != "cancelled" {
		t.Errorf("status = %q, want cancelled", got)
	}

	// And the slot is free again.
	if w := bookingRequest(t, h, http.MethodPost, "/v1/booking", studioOrigin, body); w.Code != http.StatusCreated {
		t.Errorf("rebooking a cancelled slot = %d, want 201: %s", w.Code, w.Body)
	}
}

func TestBookingAnswersTheStudioSiteAndNobodyElse(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	tests := []struct {
		name    string
		origin  string
		allowed bool
	}{
		{"the studio site", "https://studio.bykami.id", true},
		{"the root site", "https://bykami.id", true},
		{"the booth site", "https://booth.bykami.id", true},
		{"an attacker's page", "https://evil.example.com", false},
		// The dot in the suffix check is what stops this one. Without it,
		// "notbykami.id" ends with "bykami.id" and is allowed.
		{"a lookalike domain", "https://notbykami.id", false},
		{"a subdomain of a lookalike", "https://studio.bykami.id.evil.com", false},
		// http, not https. A booking posted over plain http is a booking read in
		// transit, and no site here is served that way.
		{"plain http", "http://studio.bykami.id", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := bookingRequest(t, h, http.MethodGet, "/v1/booking/services", tc.origin, "")
			got := w.Header().Get("Access-Control-Allow-Origin")
			if tc.allowed && got != tc.origin {
				t.Errorf("Allow-Origin = %q, want %q", got, tc.origin)
			}
			if !tc.allowed && got != "" {
				t.Errorf("Allow-Origin = %q, want nothing for %s", got, tc.origin)
			}
			// Never a wildcard. These routes write, and * on a POST that creates a
			// booking lets any page on the internet fill the studio's calendar.
			if got == "*" {
				t.Error("Allow-Origin is a wildcard")
			}
		})
	}
}

func TestThePreflightIsAnswered(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	// A JSON POST from a browser is preflighted, and ServeMux routes on method, so
	// without an explicit OPTIONS handler this is a 405 — which shows up as a
	// booking form that fails with nothing in the network tab to explain it.
	w := bookingRequest(t, h, http.MethodOptions, "/v1/booking", studioOrigin, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != studioOrigin {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("preflight allows no headers, so Content-Type: application/json cannot be sent")
	}
	// Vary, because availability and the catalogue are cached at the edge: a
	// response carrying one site's origin must not be handed to another's.
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want Origin", got)
	}

	if w := bookingRequest(t, h, http.MethodOptions, "/v1/booking", "https://evil.example.com", ""); w.Code != http.StatusForbidden {
		t.Errorf("preflight from an unknown origin = %d, want 403", w.Code)
	}
}

func TestBookingIsNotGatedByAuthDelivery(t *testing.T) {
	// authEnabled false is the deployed state: there is no OTP sender. Booking has
	// never required a login, so gating it the way /v1/auth/* is gated would mean
	// the studio cannot take a booking at all until a WhatsApp provider exists.
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	if w := bookingRequest(t, h, http.MethodGet, "/v1/booking/services", studioOrigin, ""); w.Code != http.StatusOK {
		t.Errorf("catalogue with auth off = %d, want 200", w.Code)
	}
	body := `{"service":"photobox-y2k","starts_at":"` + nextSlot().Format(time.RFC3339) +
		`","headcount":2,"name":"Rina","phone":"081234567890","terms":true}`
	if w := bookingRequest(t, h, http.MethodPost, "/v1/booking", studioOrigin, body); w.Code != http.StatusCreated {
		t.Errorf("booking with auth off = %d, want 201", w.Code)
	}
}

func TestBookingRefusesAFormPost(t *testing.T) {
	h, _, db, _ := newTestAPI(t, false)
	seedBooking(t, db)

	// A cross-origin HTML form can POST without a preflight, but only with a form
	// content type. Requiring application/json is what keeps those requests out,
	// and it matters more here than anywhere else in this package: booking has no
	// session, so there is no token to check instead.
	r := httptest.NewRequest(http.MethodPost, "/v1/booking",
		strings.NewReader(`service=photobox-y2k&headcount=2`))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post = %d, want 415", w.Code)
	}
}
