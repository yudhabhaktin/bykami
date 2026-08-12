package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/phone"
)

// The booking surface, and the first cross-origin one in this package.
//
// Everything else here is read by the booth kiosk or the operator console, both
// of which send a token and neither of which is a browser on another host. Booking
// is read by studio.bykami.id — a static site on Cloudflare Pages, a different
// origin from app.bykami.id — so these routes need CORS, and the preflight is not
// optional: decode() requires Content-Type: application/json, which is precisely
// the header that takes a POST out of the set a form can send without one.
//
// That requirement is doing double duty. It is what makes the preflight happen,
// and it is what stops a cross-origin form from reaching these routes at all,
// which is the CSRF shape that matters for an endpoint with no session to steal.

const (
	// availabilityLimit caps how long a range one request may ask about. The
	// booking window is 31 days and a client has no reason to ask for more; an
	// unbounded range is a way to make the server walk a year of half hours.
	availabilityLimit = 40 * 24 * time.Hour
	// bookingBody is larger than the auth bodies: a booking carries a name, an
	// email and a free-text note.
	bookingBody = 8 << 10
)

type serviceBody struct {
	ID              string `json:"id"`
	Resource        string `json:"resource"`
	Name            string `json:"name"`
	ServiceLine     string `json:"service_line"`
	Description     string `json:"description,omitempty"`
	PriceIDR        int64  `json:"price_idr"`
	PricePerPerson  bool   `json:"price_per_person"`
	DurationMinutes int    `json:"duration_minutes"`
	HeadcountMin    int    `json:"headcount_min"`
	HeadcountMax    int    `json:"headcount_max"`
	// "web" offers slots; "chat" is listed and priced but arranged over
	// WhatsApp. Always sent, never omitted: a page that has to guess the mode
	// from a missing field will guess "web" and render a calendar that cannot
	// work.
	BookingMode string `json:"booking_mode"`
	// The walls this package may be shot against, in the order it presents them.
	// Sent as [] rather than omitted for the same reason booking_mode is always
	// present: "this package has no backdrop choice" and "this response predates
	// backdrops" are different facts, and a client that cannot tell them apart
	// will render an empty picker for the photobox.
	Backdrops []backdropBody `json:"backdrops"`
}

type backdropBody struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type servicesResponse struct {
	Services []serviceBody `json:"services"`
}

// bookingServices is the catalogue: what can be booked, for how much, for how
// many.
//
// Public and cacheable. It is the same information the studio prints on a price
// list, it carries nothing about any customer, and the page that reads it is
// static — so five minutes at the edge is five minutes this box does not spend
// answering the same question.
func (a *API) bookingServices(w http.ResponseWriter, r *http.Request) {
	services, err := a.booking.Services(r.Context())
	if err != nil {
		a.internal(w, "booking services", err)
		return
	}

	out := servicesResponse{Services: make([]serviceBody, 0, len(services))}
	for _, s := range services {
		walls := make([]backdropBody, 0, len(s.Backdrops))
		for _, b := range s.Backdrops {
			walls = append(walls, backdropBody{ID: b.ID, Name: b.Name})
		}
		out.Services = append(out.Services, serviceBody{
			ID:              s.ID,
			Resource:        s.ResourceID,
			Name:            s.Name,
			ServiceLine:     s.ServiceLine,
			Description:     s.Description,
			PriceIDR:        s.PriceIDR,
			PricePerPerson:  s.PricePerPerson,
			DurationMinutes: s.DurationMinutes,
			HeadcountMin:    s.HeadcountMin,
			HeadcountMax:    s.HeadcountMax,
			BookingMode:     s.BookingMode,
			Backdrops:       walls,
		})
	}

	w.Header().Set("Cache-Control", "public, max-age=300")
	a.write(w, http.StatusOK, out)
}

type slotBody struct {
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

type availabilityResponse struct {
	Service string     `json:"service"`
	Slots   []slotBody `json:"slots"`
}

// bookingAvailability lists the times a service can be started.
//
// Cached for a minute, which is a deliberate trade rather than an oversight. The
// alternative — no-store — puts every keystroke of date-picking on a 2 vCPU box in
// Singapore, and the cost of a stale minute is bounded: a customer who picks a
// slot that has since gone is told so when they book, because Book re-checks
// against the database rather than trusting this answer.
func (a *API) bookingAvailability(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := q.Get("service")
	if service == "" {
		a.fail(w, http.StatusBadRequest, "service is required")
		return
	}

	from, ok := a.day(w, q.Get("from"))
	if !ok {
		return
	}
	to, ok := a.day(w, q.Get("to"))
	if !ok {
		return
	}
	if !to.After(from) {
		a.fail(w, http.StatusBadRequest, "to must be after from")
		return
	}
	if to.Sub(from) > availabilityLimit {
		a.fail(w, http.StatusBadRequest, "range is longer than the booking window")
		return
	}

	slots, err := a.booking.Availability(r.Context(), service, from, to)
	switch {
	case err == nil:
	case errors.Is(err, booking.ErrNoService):
		a.fail(w, http.StatusNotFound, "no such service")
		return
	// 409 rather than 404: the service exists and is on sale, it just does not
	// sell through a calendar.
	case errors.Is(err, booking.ErrChatOnly):
		a.fail(w, http.StatusConflict, "paket itu diatur lewat chat")
		return
	default:
		a.internal(w, "booking availability", err)
		return
	}

	out := availabilityResponse{Service: service, Slots: make([]slotBody, 0, len(slots))}
	for _, s := range slots {
		out.Slots = append(out.Slots, slotBody{
			// In WIB, with its offset. The client renders these directly and the
			// studio's day is a local fact; handing a browser UTC and asking it to
			// convert is one timezone conversion too many for a booking grid.
			StartsAt: s.StartsAt.In(booking.WIB).Format(time.RFC3339),
			EndsAt:   s.EndsAt.In(booking.WIB).Format(time.RFC3339),
		})
	}

	w.Header().Set("Cache-Control", "public, max-age=60")
	a.write(w, http.StatusOK, out)
}

// day parses a YYYY-MM-DD query parameter as local midnight.
//
// Date only, not an instant: the client is choosing days on a calendar, and
// accepting a full timestamp would invite a browser to send its own timezone's
// midnight and ask about the wrong day.
func (a *API) day(w http.ResponseWriter, raw string) (time.Time, bool) {
	if raw == "" {
		a.fail(w, http.StatusBadRequest, "from and to are required, as YYYY-MM-DD")
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", raw, booking.WIB)
	if err != nil {
		a.fail(w, http.StatusBadRequest, "dates must be YYYY-MM-DD")
		return time.Time{}, false
	}
	return t, true
}

type bookingRequest struct {
	Service   string `json:"service"`
	StartsAt  string `json:"starts_at"`
	Headcount int    `json:"headcount"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	Email     string `json:"email"`
	Notes     string `json:"notes"`
	Terms     bool   `json:"terms"`
	// The backdrop id, from the package's own list. Required where that list is
	// not empty — see booking.ErrBackdropRequired for why an unanswered choice is
	// refused rather than defaulted.
	Backdrop string `json:"backdrop"`
}

type bookingBodyResponse struct {
	ID        string `json:"id"`
	Service   string `json:"service"`
	StartsAt  string `json:"starts_at"`
	EndsAt    string `json:"ends_at"`
	Headcount int    `json:"headcount"`
	Name      string `json:"name"`
	Phone     string `json:"phone"`
	Status    string `json:"status"`
	// The wall's name rather than its id, because the only thing that reads this
	// prints it: a confirmation screen saying "Ivory" is the customer's record of
	// what they asked the studio to hang.
	Backdrop string `json:"backdrop"`
}

func newBookingBody(b booking.Booking) bookingBodyResponse {
	return bookingBodyResponse{
		ID:        b.ID,
		Service:   b.ServiceID,
		StartsAt:  b.StartsAt.In(booking.WIB).Format(time.RFC3339),
		EndsAt:    b.EndsAt.In(booking.WIB).Format(time.RFC3339),
		Headcount: b.Headcount,
		Name:      b.Name,
		Phone:     b.Phone,
		Status:    b.Status,
		Backdrop:  b.Backdrop,
	}
}

// createBooking takes a booking. No session, on purpose.
//
// The pages this replaces asked for a name, a number and an email and nothing
// else, and putting a login in front of a booking is the surest way to lose one.
// The number still becomes an account through identity.EnsureUser, so loyalty has
// something to attach to later — what is skipped is proving possession of it, and
// the studio already lives with that: it phones people who do not turn up.
func (a *API) createBooking(w http.ResponseWriter, r *http.Request) {
	var req bookingRequest
	if !a.decodeLarge(w, r, &req) {
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		a.fail(w, http.StatusBadRequest, "nama wajib diisi")
		return
	}
	// The old form made this mandatory, and it is where the late-cancellation
	// charge is agreed. Recording the booking without it would leave the studio's
	// own policy resting on nothing.
	if !req.Terms {
		a.fail(w, http.StatusBadRequest, "syarat dan ketentuan harus disetujui")
		return
	}

	e164, err := phone.Normalize(req.Phone)
	if err != nil {
		a.fail(w, http.StatusBadRequest, "nomor WhatsApp tidak valid")
		return
	}

	start, err := time.Parse(time.RFC3339, req.StartsAt)
	if err != nil {
		a.fail(w, http.StatusBadRequest, "starts_at must be an RFC3339 timestamp")
		return
	}

	// Best effort. A booking is the thing the customer is waiting for, and an
	// account is bookkeeping — if the upsert fails the booking still stands, with
	// no user attached, and the phone number in the row is enough to attach it
	// later.
	var userID string
	if u, err := a.identity.EnsureUser(r.Context(), e164, req.Name, req.Email); err == nil {
		userID = u.ID
	} else if !errors.Is(err, phone.ErrInvalid) {
		a.log.Warn("booking: ensure user", "err", err)
	}

	b, err := a.booking.Book(r.Context(), booking.Request{
		ServiceID:  req.Service,
		StartsAt:   start,
		Headcount:  req.Headcount,
		Name:       strings.TrimSpace(req.Name),
		Phone:      e164,
		Email:      strings.TrimSpace(req.Email),
		Notes:      strings.TrimSpace(req.Notes),
		UserID:     userID,
		BackdropID: strings.TrimSpace(req.Backdrop),
	})

	switch {
	case err == nil:
		a.write(w, http.StatusCreated, newBookingBody(b))
	case errors.Is(err, booking.ErrNoService):
		a.fail(w, http.StatusNotFound, "paket tidak ditemukan")
	case errors.Is(err, booking.ErrChatOnly):
		a.fail(w, http.StatusConflict, "paket itu diatur lewat chat, bukan lewat halaman ini")
	// 409 rather than 400: the request was well formed and would have worked a
	// moment earlier. A client should offer another time, not correct anything.
	case errors.Is(err, booking.ErrSlotTaken):
		a.fail(w, http.StatusConflict, "jadwal itu sudah terisi, silakan pilih waktu lain")
	case errors.Is(err, booking.ErrNotBookable):
		a.fail(w, http.StatusConflict, "studio tidak buka pada waktu itu")
	case errors.Is(err, booking.ErrTooSoon):
		a.fail(w, http.StatusConflict, "waktu itu sudah terlalu dekat")
	case errors.Is(err, booking.ErrTooFar):
		a.fail(w, http.StatusBadRequest, "waktu itu terlalu jauh ke depan")
	case errors.Is(err, booking.ErrHeadcount):
		a.fail(w, http.StatusBadRequest, "jumlah orang tidak sesuai paket")
	case errors.Is(err, booking.ErrBackdropRequired):
		a.fail(w, http.StatusBadRequest, "pilih background dulu")
	// 400 rather than 409: unlike a taken slot, this did not become wrong while
	// the customer was filling the form. Either the page is stale or the value was
	// made up, and both call for a reload rather than another time.
	case errors.Is(err, booking.ErrBackdropUnknown):
		a.fail(w, http.StatusBadRequest, "background itu tidak tersedia untuk paket ini")
	case errors.Is(err, booking.ErrNotOnTheGrid):
		a.fail(w, http.StatusBadRequest, "starts_at must be on the half hour")
	default:
		a.internal(w, "create booking", err)
	}
}

// readBooking is the confirmation page's own read.
//
// The id is 128 bits of crypto/rand, which is what stands in for a credential
// here — the same shape as an unguessable link in a confirmation email. Personal
// data, so no-store, which is what write does by default.
func (a *API) readBooking(w http.ResponseWriter, r *http.Request) {
	b, err := a.booking.Get(r.Context(), r.PathValue("id"))
	switch {
	case err == nil:
		a.write(w, http.StatusOK, newBookingBody(b))
	case errors.Is(err, booking.ErrNoBooking):
		a.fail(w, http.StatusNotFound, "booking tidak ditemukan")
	default:
		a.internal(w, "read booking", err)
	}
}

type cancelRequest struct {
	Phone string `json:"phone"`
}

// cancelBooking releases a slot.
//
// The phone number has to match the one that booked. It is weak proof, and it is
// the proof the booking form collected — asking for more would mean the customer
// cannot cancel at all without an account they were never made to create. What it
// buys is that knowing an id is not enough on its own.
//
// The late-cancellation charge is not enforced here. It is a policy the studio
// applies at the desk, and a system that refused the cancellation would only
// produce a no-show instead of a freed slot.
func (a *API) cancelBooking(w http.ResponseWriter, r *http.Request) {
	var req cancelRequest
	if !a.decodeLarge(w, r, &req) {
		return
	}
	e164, err := phone.Normalize(req.Phone)
	if err != nil {
		a.fail(w, http.StatusBadRequest, "nomor WhatsApp tidak valid")
		return
	}

	b, err := a.booking.Cancel(r.Context(), r.PathValue("id"), e164)
	switch {
	case err == nil:
		a.write(w, http.StatusOK, newBookingBody(b))
	case errors.Is(err, booking.ErrNoBooking):
		a.fail(w, http.StatusNotFound, "booking tidak ditemukan")
	default:
		a.internal(w, "cancel booking", err)
	}
}

// cors answers a browser on one of the four sites.
//
// An allowlist and never a wildcard: these routes write, and
// Access-Control-Allow-Origin: * on a POST that creates a booking would let any
// page on the internet fill the studio's calendar. Vary: Origin because the
// catalogue and availability are cached at the edge, and a response carrying one
// site's origin must not be handed to another's.
func (a *API) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); a.originAllowed(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
		}
		next(w, r)
	}
}

// preflight answers the OPTIONS that a JSON POST from a browser requires.
//
// Registered explicitly because ServeMux routes on method: an OPTIONS to a
// pattern registered for POST is a 405, and a 405 to a preflight is a booking
// form that fails with nothing in the network tab that explains it.
func (a *API) preflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !a.originAllowed(origin) {
		a.fail(w, http.StatusForbidden, "origin not allowed")
		return
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type")
	h.Set("Access-Control-Max-Age", "600")
	h.Add("Vary", "Origin")
	w.WriteHeader(http.StatusNoContent)
}

// originAllowed matches the four sites, plus whatever a developer was handed on
// the command line.
//
// Matched by suffix rather than held as a list of four, because the list already
// exists in packages/content and a second copy here would be a fifth site that
// silently cannot book. Only https, and only the real domain or a subdomain of
// it — "evilbykami.id" must not pass, which is what the dot in the suffix is for.
func (a *API) originAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	for _, extra := range a.bookingOrigins {
		if origin == extra {
			return true
		}
	}
	host, ok := strings.CutPrefix(origin, "https://")
	if !ok {
		return false
	}
	// No port, no path, no userinfo. An Origin header has none of those for a
	// normal https page, and anything carrying them is not one of the four sites.
	if strings.ContainsAny(host, ":/@") {
		return false
	}
	return host == "bykami.id" || strings.HasSuffix(host, ".bykami.id")
}

// decodeLarge is decode with the booking body limit.
//
// A booking carries a name, an email and a free-text note, none of which fit
// comfortably in the 4KB an auth request needs. The reader is installed here and
// decode's own MaxBytesReader then wraps this one — the inner limit is the smaller
// of the two and wins, so decode takes the limit as an argument rather than
// letting a caller believe it raised a ceiling it did not.
func (a *API) decodeLarge(w http.ResponseWriter, r *http.Request, dst any) bool {
	return a.decodeLimited(w, r, dst, bookingBody)
}
