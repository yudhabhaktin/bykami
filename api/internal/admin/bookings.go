package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
)

// The operator's day. One screen, one date, and the two things somebody standing
// at the studio actually does: see who is coming, and close time off.
//
// Deliberately not a month grid. The person reading this is between sessions on a
// phone, and a month of half-hour slots on a 390px screen is a wall — a day is
// what they can act on. The calendar view for planning ahead is Google Calendar,
// which is where the owner already works and which every booking is written into.

// bookingDay lists a day's bookings, and reports whether the calendars are
// syncing.
func (c *Console) bookingDay(w http.ResponseWriter, r *http.Request, op identity.User) {
	day, err := parseDay(r.URL.Query().Get("day"))
	if err != nil {
		c.backToBookings(w, r, time.Now().In(wib), "Tanggal tidak valid.")
		return
	}
	if day.IsZero() {
		day = time.Now().In(wib)
	}

	p := page{
		Title:       "Booking",
		Operator:    op.Phone,
		AuthEnabled: c.authEnabled,
		CSRF:        csrfToken(r),
		Day:         day.In(wib),
		DayISO:      day.In(wib).Format("2006-01-02"),
		PrevDay:     day.AddDate(0, 0, -1).In(wib).Format("2006-01-02"),
		NextDay:     day.AddDate(0, 0, 1).In(wib).Format("2006-01-02"),
	}
	if msg := r.URL.Query().Get("ok"); msg != "" {
		p.Notice = msg
	}
	if msg := r.URL.Query().Get("err"); msg != "" {
		p.Error = msg
	}

	list, err := c.booking.Day(r.Context(), day)
	if err != nil {
		c.log.Error("admin: list bookings", "err", err)
		p.Error = "Gagal memuat booking."
		c.render(w, r, http.StatusInternalServerError, "bookings.html", p)
		return
	}

	// The service name, not its id. An operator reading "self-midi" has to
	// translate; "MIDI" is what is written on the price list and said out loud.
	services, err := c.booking.Services(r.Context())
	if err == nil {
		names := make(map[string]string, len(services))
		for _, s := range services {
			names[s.ID] = s.Name
		}
		for i := range list {
			if n, ok := names[list[i].ServiceID]; ok {
				list[i].ServiceID = n
			}
		}
	}
	p.Bookings = list

	// Surfaced on the page an operator opens daily rather than hidden in a log,
	// because the failure it reports is silent by design: a calendar that stopped
	// syncing leaves the booth selling slots the owner has blocked, and the only
	// symptom is a double booking weeks later.
	if state, err := c.booking.SyncState(r.Context()); err == nil {
		for _, s := range state {
			row := calendarRow{Resource: s.ResourceID, Calendar: s.CalendarID, Error: s.Error}
			if !s.FetchedAt.IsZero() {
				row.Synced = s.FetchedAt.In(wib).Format("2006-01-02 15:04")
				row.Stale = time.Since(s.FetchedAt) > 30*time.Minute
			}
			p.Calendars = append(p.Calendars, row)
		}
	}

	c.render(w, r, http.StatusOK, "bookings.html", p)
}

// bookingCancel releases a slot on the customer's behalf.
//
// No phone number is checked here, unlike the customer's own cancellation: the
// operator has already been authenticated and is standing in front of whoever is
// asking. Every one is logged with the operator's number, the same as a loyalty
// adjustment, because this is money the studio is choosing not to take.
func (c *Console) bookingCancel(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}

	day, err := parseDay(r.FormValue("day"))
	if err != nil || day.IsZero() {
		day = time.Now().In(wib)
	}

	id := r.PathValue("id")
	b, err := c.booking.Cancel(r.Context(), id, "")
	switch {
	case err == nil:
	case errors.Is(err, booking.ErrNoBooking):
		c.backToBookings(w, r, day, "Booking tidak ditemukan.")
		return
	default:
		c.log.Error("admin: cancel booking", "operator", op.Phone, "booking", id, "err", err)
		c.backToBookings(w, r, day, "Gagal membatalkan booking.")
		return
	}

	c.log.Info("admin: booking cancelled", "operator", op.Phone, "booking", b.ID,
		"phone", b.Phone, "starts_at", b.StartsAt)
	c.redirect(w, r, "/bookings?day="+day.In(wib).Format("2006-01-02")+
		"&ok="+urlQueryEscape(fmt.Sprintf("Booking %s dibatalkan.", b.Name)))
}

// bookingBlock closes a range of a day, or the whole day.
//
// Hours are taken as HH:MM in WIB and the end is exclusive, because a blackout is
// a span rather than a set of days — the inclusive-date translation that frame
// seasons need does not apply, and pretending otherwise would close an extra half
// hour nobody asked to close.
func (c *Console) bookingBlock(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}

	day, err := parseDay(r.FormValue("day"))
	if err != nil || day.IsZero() {
		c.backToBookings(w, r, time.Now().In(wib), "Tanggal tidak valid.")
		return
	}

	from, to := strings.TrimSpace(r.FormValue("from")), strings.TrimSpace(r.FormValue("to"))
	start, end := day, day.AddDate(0, 0, 1)
	if from != "" || to != "" {
		start, err = atClock(day, from)
		if err != nil {
			c.backToBookings(w, r, day, "Jam mulai tidak valid. Gunakan format 14:00.")
			return
		}
		end, err = atClock(day, to)
		if err != nil {
			c.backToBookings(w, r, day, "Jam selesai tidak valid. Gunakan format 14:00.")
			return
		}
	}
	if !end.After(start) {
		c.backToBookings(w, r, day, "Jam selesai harus setelah jam mulai.")
		return
	}

	// Empty means every resource, which is what "closed today" means and is the
	// entry an operator reaches for most.
	resource := r.FormValue("resource")
	reason := strings.TrimSpace(r.FormValue("reason"))

	if err := c.booking.Blackout(r.Context(), resource, start, end, reason); err != nil {
		c.log.Error("admin: blackout", "operator", op.Phone, "err", err)
		c.backToBookings(w, r, day, "Gagal menutup jadwal.")
		return
	}

	c.log.Info("admin: schedule blocked", "operator", op.Phone, "resource", resource,
		"from", start, "to", end, "reason", reason)
	c.redirect(w, r, "/bookings?day="+day.In(wib).Format("2006-01-02")+
		"&ok="+urlQueryEscape("Jadwal ditutup."))
}

// atClock reads HH:MM against a local day. An empty time means midnight, so a
// half-open form — a start with no end — closes the rest of the day.
func atClock(day time.Time, hhmm string) (time.Time, error) {
	local := day.In(wib)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, wib)
	if hhmm == "" {
		return midnight.AddDate(0, 0, 1), nil
	}
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	return midnight.Add(time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute), nil
}

func (c *Console) backToBookings(w http.ResponseWriter, r *http.Request, day time.Time, msg string) {
	c.redirect(w, r, "/bookings?day="+day.In(wib).Format("2006-01-02")+"&err="+urlQueryEscape(msg))
}
