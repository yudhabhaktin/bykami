package booking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ErrEventGone means a calendar event is already absent. An implementation must
// return it rather than a generic failure, or a booking cancelled after its event
// was deleted by hand is retried on every tick forever.
var ErrEventGone = errors.New("booking: calendar event no longer exists")

// Event is a booking as the owner's calendar should show it.
type Event struct {
	Summary     string
	Description string
	Location    string
	StartsAt    time.Time
	EndsAt      time.Time
}

// Calendar is the owner's calendar, as much of it as booking needs.
//
// An interface, and one owning its own types, for the reason identity.Sender is:
// the choice of provider is not this package's business, and stating the
// dependency in primitives is what lets the sync loop be tested without a
// network. internal/gcal is the implementation; cmd/bykami bridges the two.
type Calendar interface {
	FreeBusy(ctx context.Context, calendarID string, from, to time.Time) ([]Busy, error)
	Insert(ctx context.Context, calendarID string, ev Event) (string, error)
	Delete(ctx context.Context, calendarID, eventID string) error
}

// Principal is implemented by a Calendar that authenticates as a named account —
// the address each calendar has to be shared with before any of this works.
//
// Optional, and separate from Calendar, because booking genuinely does not care
// how the calendar authenticates: nothing in the sync loop reads this. The
// operator console does, so that the one manual step in the whole setup is
// printed on the page where it has to be carried out rather than dug out of a
// JSON file on a server.
type Principal interface {
	ServiceAccount() string
}

const (
	// DefaultInterval is how often the owner's calendar is re-read. A slot the
	// owner blocks by hand is offered for up to this long afterwards, which is why
	// it is minutes and not hours — and why Book re-checks before writing.
	DefaultInterval = 5 * time.Minute
	// How many bookings to mirror per pass. Bounded so a backlog cannot turn one
	// tick into hundreds of API calls on a 2 vCPU box.
	mirrorLimit = 25
)

// Worker keeps the database and the owner's calendar agreeing.
//
// Three jobs, in one pass: read back what the owner blocked by hand, write out
// bookings that are not in the calendar yet, and remove events for bookings that
// were cancelled. None of them is on a request path — a customer's booking is
// confirmed by the database and this catches up afterwards.
type Worker struct {
	desk     *Desk
	cal      Calendar
	log      *slog.Logger
	interval time.Duration
	// location is put on every event, so the owner's calendar entry is something
	// they can navigate from.
	location string
}

// NewWorker returns nil when there is no calendar to talk to.
//
// Nil rather than an error: a deployment with no Google credentials is the
// ordinary state of a fresh box, and booking has to work without one. Callers
// check for nil the same way cmd/bykami already does for the Instagram mirror.
func NewWorker(d *Desk, cal Calendar, log *slog.Logger, interval time.Duration, location string) *Worker {
	if cal == nil {
		return nil
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Worker{desk: d, cal: cal, log: log, interval: interval, location: location}
}

// ServiceAccount is the address the studio's calendars must be shared with, or
// empty if the calendar cannot say. Read by the console, never by the loop.
func (w *Worker) ServiceAccount() string {
	if p, ok := w.cal.(Principal); ok {
		return p.ServiceAccount()
	}
	return ""
}

// SyncNow runs one pass on demand, bounded.
//
// This is the console's "test it now" button, and the bound is what makes it safe
// to answer an HTTP request with: three unreachable calendars at the client's own
// 30-second timeout would outlast the server's write deadline, and the operator
// would get a dead connection instead of the error Google actually returned.
//
// Errors per resource are not returned — they are recorded against each one, the
// same as any scheduled pass, so the page that triggered this shows them in the
// table it already has.
func (w *Worker) SyncNow(ctx context.Context, within time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	return w.Sync(ctx)
}

// Run syncs until the context is cancelled.
func (w *Worker) Run(ctx context.Context) {
	// Once at startup, so a box that has just come up is not serving a stale
	// calendar for the first interval.
	if err := w.Sync(ctx); err != nil {
		w.log.Warn("booking: calendar sync", "err", err)
	}

	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.Sync(ctx); err != nil {
				w.log.Warn("booking: calendar sync", "err", err)
			}
		}
	}
}

// Sync runs one pass.
//
// Per-resource and per-booking failures are logged and stepped over rather than
// returned: one calendar that has not been shared yet must not stop the other two
// from syncing, and one event Google refuses must not block the queue behind it.
// An error comes back only when the database itself could not be read, which is
// not a condition retrying helps.
func (w *Worker) Sync(ctx context.Context) error {
	resources, err := w.desk.Resources(ctx)
	if err != nil {
		return err
	}

	now := w.desk.now().UTC()
	// An hour behind, so a session running right now still reads as busy.
	from := now.Add(-time.Hour)
	to := now.Add(w.desk.window)

	for _, r := range resources {
		if r.GoogleCalendarID == "" {
			continue
		}
		busy, err := w.cal.FreeBusy(ctx, r.GoogleCalendarID, from, to)
		if err != nil {
			// The cached ranges stay in place. See the migration on why this is a
			// cache: stale availability risks a clash the operator can resolve with
			// a phone call, and no availability loses the booking outright.
			w.log.Warn("booking: read calendar", "resource", r.ID, "err", err)
			if err := w.desk.RecordSyncFailure(ctx, r.ID, err.Error()); err != nil {
				w.log.Error("booking: record sync failure", "resource", r.ID, "err", err)
			}
			continue
		}
		if err := w.desk.ReplaceBusy(ctx, r.ID, from, to, busy); err != nil {
			w.log.Error("booking: store busy", "resource", r.ID, "err", err)
		}
	}

	w.mirror(ctx)
	w.withdraw(ctx)
	return nil
}

// mirror writes confirmed bookings into the owner's calendar.
func (w *Worker) mirror(ctx context.Context) {
	pending, err := w.desk.Unmirrored(ctx, mirrorLimit)
	if err != nil {
		w.log.Error("booking: read mirror queue", "err", err)
		return
	}

	for _, b := range pending {
		resource, err := w.desk.Resource(ctx, b.ResourceID)
		if err != nil {
			w.log.Error("booking: mirror", "booking", b.ID, "err", err)
			continue
		}
		if resource.GoogleCalendarID == "" {
			continue
		}
		svc, err := w.desk.Service(ctx, b.ServiceID)
		if err != nil {
			w.log.Error("booking: mirror", "booking", b.ID, "err", err)
			continue
		}

		id, err := w.cal.Insert(ctx, resource.GoogleCalendarID, describe(b, svc, w.location))
		if err != nil {
			// The booking is already confirmed and the customer has been told so.
			// A calendar that will not take it is a thing to retry, never a reason
			// to undo a sale.
			w.log.Warn("booking: write calendar", "booking", b.ID, "err", err)
			continue
		}
		if err := w.desk.AttachEvent(ctx, b.ID, id); err != nil {
			// Losing the id after the event was created means the next pass writes
			// a duplicate. Logged at error because that is an operator-visible mess
			// in the calendar they work from.
			w.log.Error("booking: attach event", "booking", b.ID, "event", id, "err", err)
		}
	}
}

// withdraw removes events whose bookings were cancelled.
func (w *Worker) withdraw(ctx context.Context) {
	cancelled, err := w.desk.Cancelled(ctx, mirrorLimit)
	if err != nil {
		w.log.Error("booking: read cancelled queue", "err", err)
		return
	}

	for _, b := range cancelled {
		resource, err := w.desk.Resource(ctx, b.ResourceID)
		if err != nil {
			w.log.Error("booking: withdraw", "booking", b.ID, "err", err)
			continue
		}
		err = w.cal.Delete(ctx, resource.GoogleCalendarID, b.GCalEventID)
		if err != nil && !errors.Is(err, ErrEventGone) {
			w.log.Warn("booking: delete event", "booking", b.ID, "err", err)
			continue
		}
		// Gone counts as done. An event the owner already deleted by hand is the
		// state this was trying to reach.
		if err := w.desk.ForgetEvent(ctx, b.ID); err != nil {
			w.log.Error("booking: forget event", "booking", b.ID, "err", err)
		}
	}
}

// describe renders a booking as a calendar entry.
//
// Written for the owner reading it on a phone between sessions, so the package
// and the group size lead and the contact details follow. Indonesian, because
// that is the language the studio is run in.
//
// The event spans the reservation and not the session. A photobox booking is ten
// minutes of a half-hour slot, and an event showing 14:00–14:10 would tell the
// owner that 14:10 is free when the system is holding it until 14:30.
func describe(b Booking, svc Service, location string) Event {
	slotEnd := b.StartsAt.Add(svc.span())

	var sb strings.Builder
	fmt.Fprintf(&sb, "Paket: %s\n", svc.Name)
	// Second line, above the customer's own details, because it is the only line
	// here that is an instruction: the roll has to be hung before the session
	// starts, and the owner reads this event on a phone between sessions. Absent
	// entirely for a package with no choice rather than printed empty.
	if b.Backdrop != "" {
		fmt.Fprintf(&sb, "Background: %s\n", b.Backdrop)
	}
	fmt.Fprintf(&sb, "Nama: %s\n", b.Name)
	fmt.Fprintf(&sb, "WhatsApp: %s\n", b.Phone)
	if b.Email != "" {
		fmt.Fprintf(&sb, "Email: %s\n", b.Email)
	}
	fmt.Fprintf(&sb, "Jumlah: %d orang\n", b.Headcount)
	fmt.Fprintf(&sb, "Sesi: %s–%s (slot sampai %s)\n",
		b.StartsAt.In(WIB).Format("15:04"),
		b.EndsAt.In(WIB).Format("15:04"),
		slotEnd.In(WIB).Format("15:04"))
	if b.Notes != "" {
		fmt.Fprintf(&sb, "Catatan: %s\n", b.Notes)
	}
	fmt.Fprintf(&sb, "Kode booking: %s", b.ID)

	return Event{
		Summary:     fmt.Sprintf("%s · %s (%d orang)", svc.Name, b.Name, b.Headcount),
		Description: sb.String(),
		Location:    location,
		StartsAt:    b.StartsAt,
		EndsAt:      slotEnd,
	}
}
