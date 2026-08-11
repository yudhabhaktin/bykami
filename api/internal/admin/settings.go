package admin

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
)

// Connecting the studio's Google Calendars.
//
// Three things have to be true before booking knows when the owner is busy: the
// service-account key is in the environment, each calendar is shared with that
// account, and each resource here points at a calendar id. Only the third is a
// thing a console can do — the first is a file on the server and the second
// happens inside Google.
//
// So this page does the third, and makes the other two *legible*: it prints the
// address to share the calendars with, and it has a button that runs a real sync
// and shows what Google said. Before it existed, connecting a calendar meant a
// shell on the VPS, and diagnosing a failure meant reading the journal.

// syncBudget bounds the on-demand sync. Comfortably inside the server's 30-second
// write timeout even when every calendar is unreachable.
const syncBudget = 12 * time.Second

func (c *Console) settings(w http.ResponseWriter, r *http.Request, op identity.User) {
	p := page{
		Title:    "Pengaturan",
		Operator: op.Phone,
		CSRF:     csrfToken(r),
	}
	if msg := r.URL.Query().Get("ok"); msg != "" {
		p.Notice = msg
	}
	if msg := r.URL.Query().Get("err"); msg != "" {
		p.Error = msg
	}

	// Empty when no credential is configured, which is a different failure from a
	// calendar that is not shared and has to read differently on the page: there
	// is nothing to share the calendar *with* yet.
	if c.calendar != nil {
		p.ServiceAccount = c.calendar.ServiceAccount()
	}
	p.GoogleConnect = c.connect != nil && c.calendar != nil

	resources, err := c.booking.Resources(r.Context())
	if err != nil {
		c.log.Error("admin: list resources", "err", err)
		p.Error = "Gagal memuat daftar ruang."
		c.render(w, r, http.StatusInternalServerError, "settings.html", p)
		return
	}

	state, err := c.booking.SyncState(r.Context())
	if err != nil {
		c.log.Error("admin: sync state", "err", err)
	}
	synced := make(map[string]booking.Sync, len(state))
	for _, s := range state {
		synced[s.ResourceID] = s
	}

	for _, res := range resources {
		row := calendarRow{Resource: res.ID, Name: res.Name, Calendar: res.GoogleCalendarID}
		if s, ok := synced[res.ID]; ok {
			row.Error = s.Error
			if !s.FetchedAt.IsZero() {
				row.Synced = s.FetchedAt.In(wib).Format("2006-01-02 15:04")
				row.Stale = time.Since(s.FetchedAt) > 30*time.Minute
			}
		}
		p.Calendars = append(p.Calendars, row)
	}

	c.render(w, r, http.StatusOK, "settings.html", p)
}

// settingsCalendar points one resource at a calendar, or detaches it.
func (c *Console) settingsCalendar(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}

	resource := r.PathValue("id")
	calendarID := r.FormValue("calendar_id")

	err := c.booking.SetCalendar(r.Context(), resource, calendarID)
	switch {
	case err == nil:
	case errors.Is(err, booking.ErrBadCalendarID):
		// The real message, not a generic one. "You pasted a name where an id
		// goes" is the mistake, and saying so is the whole value of the check.
		c.backToSettings(w, r, err.Error())
		return
	default:
		c.log.Error("admin: set calendar", "operator", op.Phone, "resource", resource, "err", err)
		c.backToSettings(w, r, "Gagal menyimpan kalender.")
		return
	}

	c.log.Info("admin: calendar set", "operator", op.Phone, "resource", resource,
		"calendar", calendarID)

	if calendarID == "" {
		c.redirect(w, r, "/settings?ok="+urlQueryEscape(
			fmt.Sprintf("Kalender %s dilepas.", resource)))
		return
	}
	c.redirect(w, r, "/settings?ok="+urlQueryEscape(fmt.Sprintf(
		"Kalender %s tersimpan. Tekan “Tes sinkron sekarang” untuk memastikan sudah dibagikan.",
		resource)))
}

// settingsSync runs one sync on demand and sends the operator back to the table,
// where each resource's result is already displayed.
//
// The button exists because the alternative is waiting up to five minutes to find
// out whether a calendar was shared correctly, and reading a journal to find out
// why not.
func (c *Console) settingsSync(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	if c.calendar == nil {
		c.backToSettings(w, r, "Google Calendar belum dikonfigurasi di server.")
		return
	}

	if err := c.calendar.SyncNow(r.Context(), syncBudget); err != nil {
		c.log.Error("admin: sync now", "operator", op.Phone, "err", err)
		c.backToSettings(w, r, "Sinkronisasi gagal dijalankan.")
		return
	}

	c.log.Info("admin: sync run by hand", "operator", op.Phone)
	c.redirect(w, r, "/settings?ok="+urlQueryEscape(
		"Sinkronisasi dijalankan. Lihat status tiap ruang di bawah."))
}

func (c *Console) backToSettings(w http.ResponseWriter, r *http.Request, msg string) {
	c.redirect(w, r, "/settings?err="+urlQueryEscape(msg))
}
