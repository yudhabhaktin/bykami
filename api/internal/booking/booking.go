// Package booking sells slots at the studio.
//
// It replaces two YouCanBook.me calendars. The shape of what it offers was taken
// from those pages rather than invented: a 30-minute grid from 09:00 to 21:00
// every day, sessions of 10 to 40 minutes inside it, a break for Dzuhur that
// moves to 11:30 on Friday, and a break for Maghrib at 17:30 that does not move.
//
// Two decisions carry the package.
//
// A booking is confirmed the instant it is written, and payment is not part of
// it. The studio's own tagline is BOOKING TANPA DP and the architecture record
// keeps it: with optional prepay there is no slot to hold while a customer
// scans a QR, no timeout racing a webhook, and no reconciliation — which is
// where self-built booking systems keep their worst bugs.
//
// Double booking is prevented by the database and not by this code. Availability
// is read, shown to a customer, and acted on seconds or minutes later, so any
// check performed here is a statement about the past. booking_slots holds one
// row per half hour a session occupies with a primary key on
// (resource_id, starts_at), inserted in the booking's own transaction, so the
// second of two simultaneous requests for one room loses on a constraint rather
// than on timing. Everything this package checks before inserting exists to
// produce a good error message, not to deliver the guarantee.
package booking

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// WIB is the only timezone the studio operates in. Opening hours and breaks are
// stored as minutes past local midnight, and local means this — a server that
// reasons about 09:00 in UTC opens the studio at four in the afternoon.
var WIB = time.FixedZone("WIB", 7*60*60)

// slotMinutes is the granularity a customer chooses a time on, and the unit
// booking_slots reserves.
//
// Thirty because that is the grid the studio already sold on and the interval
// its customers are used to seeing, not because any session is that long — a
// photobox session is ten minutes. Sessions shorter than the grid still take a
// whole slot, which is what the old booking pages did and what makes the room
// ready for the next group.
const slotMinutes = 30

const (
	defaultNotice = 30 * time.Minute
	defaultWindow = 31 * 24 * time.Hour
)

var (
	// ErrNoService means the service id is not one this studio sells, or has been
	// deactivated since the page was loaded.
	ErrNoService = errors.New("booking: no such service")
	// ErrNoBooking means no booking has that id, or the phone number given does
	// not match the one that made it.
	ErrNoBooking = errors.New("booking: no such booking")
	// ErrSlotTaken means somebody else has the room. Returned when the database
	// refuses the write, which is the only authority on this.
	ErrSlotTaken = errors.New("booking: that time is already taken")
	// ErrNotBookable means the time is not one the studio offers at all: outside
	// opening hours, inside a break, or blacked out.
	ErrNotBookable = errors.New("booking: the studio is not open then")
	// ErrNotOnTheGrid means the start time is not a half-hour boundary. Rejected
	// rather than rounded, because rounding a request silently books a customer
	// at a time they did not choose.
	ErrNotOnTheGrid = errors.New("booking: start time is not on the half hour")
	// ErrTooSoon means the slot is inside the minimum notice, including any slot
	// already in the past.
	ErrTooSoon = errors.New("booking: that time is too soon")
	// ErrTooFar means the slot is past the end of the booking window.
	ErrTooFar = errors.New("booking: that time is too far ahead")
	// ErrHeadcount means the group does not fit the package it was booked on.
	ErrHeadcount = errors.New("booking: that package is not for that many people")
)

// Resource is something that can be occupied at one instant — a photobox, a
// self-photo room, a photographer with a camera bag.
type Resource struct {
	ID               string
	Name             string
	GoogleCalendarID string
}

// Service is one sellable session.
type Service struct {
	ID          string
	ResourceID  string
	Name        string
	ServiceLine string
	Description string

	PriceIDR int64
	// PricePerPerson says whether PriceIDR is per head or for the group. The
	// photobox and Pas Foto Formal are per head; everything else is not.
	PricePerPerson bool

	DurationMinutes int
	BufferMinutes   int
	HeadcountMin    int
	HeadcountMax    int
	OrderIndex      int
}

// span is how long the room is unavailable for: the session plus its changeover,
// rounded up to the grid. A ten-minute session with no buffer still costs a slot.
func (s Service) span() time.Duration {
	total := s.DurationMinutes + s.BufferMinutes
	slots := (total + slotMinutes - 1) / slotMinutes
	if slots < 1 {
		slots = 1
	}
	return time.Duration(slots) * slotMinutes * time.Minute
}

// Slot is a time a customer may choose.
type Slot struct {
	StartsAt time.Time
	EndsAt   time.Time
}

// Booking is a confirmed session, or one that was cancelled.
type Booking struct {
	ID         string
	ResourceID string
	ServiceID  string
	// UserID is the customer's account when the phone number resolved to one.
	// Empty is normal: booking asks for no login.
	UserID string

	StartsAt  time.Time
	EndsAt    time.Time
	Headcount int

	Name  string
	Phone string
	Email string
	Notes string

	Status string
	// GCalEventID is empty until the booking has been mirrored into the owner's
	// Google Calendar. Empty is not a failed booking — see internal/gcal.
	GCalEventID string

	CreatedAt   time.Time
	CancelledAt time.Time
}

// Request is what a customer submitted. Phone must already be E.164 —
// internal/phone owns that, and two spellings of one number are two customers.
type Request struct {
	ServiceID string
	StartsAt  time.Time
	Headcount int
	Name      string
	Phone     string
	Email     string
	Notes     string
	UserID    string
}

// Desk is the booking desk: what is free, and who has what.
type Desk struct {
	db *sql.DB
	// Injected so tests can sit at a fixed instant. Availability is defined
	// relative to now, so a test that used the real clock would pass in the
	// morning and fail after nine at night.
	now func() time.Time
	// notice is how far ahead of now the first bookable slot is.
	notice time.Duration
	// window is how far ahead the calendar is open at all.
	window time.Duration
}

// New builds a desk. A zero window takes the default, following the same
// convention as internal/instagram: a caller that has nothing to say about a
// knob should not have to know its value.
func New(db *sql.DB, window time.Duration) *Desk {
	if window <= 0 {
		window = defaultWindow
	}
	return &Desk{db: db, now: time.Now, notice: defaultNotice, window: window}
}

// Services lists what is on sale, grouped by resource and in display order.
func (d *Desk) Services(ctx context.Context) ([]Service, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT s.id, s.resource_id, s.name, s.service_line, s.description,
		        s.price_idr, s.price_per_person,
		        s.duration_minutes, s.buffer_minutes,
		        s.headcount_min, s.headcount_max, s.order_index
		 FROM booking_services s
		 JOIN booking_resources r ON r.id = s.resource_id
		 WHERE s.active = 1 AND r.active = 1
		 ORDER BY s.order_index, s.id`)
	if err != nil {
		return nil, fmt.Errorf("booking: services: %w", err)
	}
	defer rows.Close()

	out := make([]Service, 0, 16)
	for rows.Next() {
		s, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Service reads one, whether or not it is still on sale — a booking made
// yesterday has to stay readable after the package is withdrawn.
func (d *Desk) Service(ctx context.Context, id string) (Service, error) {
	row := d.db.QueryRowContext(ctx,
		`SELECT s.id, s.resource_id, s.name, s.service_line, s.description,
		        s.price_idr, s.price_per_person,
		        s.duration_minutes, s.buffer_minutes,
		        s.headcount_min, s.headcount_max, s.order_index
		 FROM booking_services s WHERE s.id = ?`, id)
	s, err := scanService(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Service{}, ErrNoService
	}
	return s, err
}

// Book writes a confirmed booking, or explains why it could not.
//
// The order is deliberate: everything cheap and knowable is checked first so the
// customer gets a specific message, then the insert runs and the database has
// the final word on whether the room was free. A caller must treat ErrSlotTaken
// as ordinary — it is what losing a race looks like, and on a Saturday evening it
// will happen.
func (d *Desk) Book(ctx context.Context, req Request) (Booking, error) {
	svc, err := d.Service(ctx, req.ServiceID)
	if err != nil {
		return Booking{}, err
	}

	start := req.StartsAt.UTC().Truncate(time.Second)
	if start.Truncate(slotMinutes*time.Minute) != start {
		return Booking{}, ErrNotOnTheGrid
	}
	if req.Headcount < svc.HeadcountMin || req.Headcount > svc.HeadcountMax {
		return Booking{}, fmt.Errorf("%w: %s takes %d–%d",
			ErrHeadcount, svc.Name, svc.HeadcountMin, svc.HeadcountMax)
	}

	now := d.now().UTC()
	if start.Before(now.Add(d.notice)) {
		return Booking{}, ErrTooSoon
	}
	if start.After(now.Add(d.window)) {
		return Booking{}, ErrTooFar
	}

	span := svc.span()
	points := gridPoints(start, span)

	// Opening hours, breaks and blackouts are read here rather than trusted from
	// the client, because the request carries a time and nothing else — a stale
	// page, or a crafted POST, would otherwise book the studio at four in the
	// morning.
	sched, err := d.schedule(ctx, svc.ResourceID, start, start.Add(span))
	if err != nil {
		return Booking{}, err
	}
	if !sched.open(start, span) {
		return Booking{}, ErrNotBookable
	}
	for _, p := range points {
		if sched.blocked[p.Unix()] {
			return Booking{}, ErrSlotTaken
		}
	}

	b := Booking{
		ID:         newID(),
		ResourceID: svc.ResourceID,
		ServiceID:  svc.ID,
		UserID:     req.UserID,
		StartsAt:   start,
		// The session, not the slot. What the customer is told they have, which
		// for a photobox is ten minutes of a thirty-minute reservation.
		EndsAt:    start.Add(time.Duration(svc.DurationMinutes) * time.Minute),
		Headcount: req.Headcount,
		Name:      req.Name,
		Phone:     req.Phone,
		Email:     req.Email,
		Notes:     req.Notes,
		Status:    "confirmed",
		CreatedAt: now.Truncate(time.Second),
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return Booking{}, fmt.Errorf("booking: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO bookings
		   (id, resource_id, service_id, user_id, starts_at, ends_at, headcount,
		    name, phone, email, notes, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'confirmed', ?)`,
		b.ID, b.ResourceID, b.ServiceID, nullIfEmpty(b.UserID),
		b.StartsAt.Unix(), b.EndsAt.Unix(), b.Headcount,
		b.Name, b.Phone, b.Email, b.Notes, b.CreatedAt.Unix()); err != nil {
		return Booking{}, fmt.Errorf("booking: insert: %w", err)
	}

	// The reservation itself. Every half hour the session touches, in the same
	// transaction, so a collision on any one of them takes the whole booking
	// down rather than leaving a half-reserved session behind.
	for _, p := range points {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO booking_slots (resource_id, starts_at, booking_id) VALUES (?, ?, ?)`,
			b.ResourceID, p.Unix(), b.ID)
		if err != nil {
			if store.IsConstraint(err) {
				return Booking{}, ErrSlotTaken
			}
			return Booking{}, fmt.Errorf("booking: reserve: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		// SQLite reports a losing writer here as well as on the insert above,
		// depending on where the conflict was noticed.
		if store.IsConstraint(err) {
			return Booking{}, ErrSlotTaken
		}
		return Booking{}, fmt.Errorf("booking: commit: %w", err)
	}
	return b, nil
}

// Get reads a booking by id.
func (d *Desk) Get(ctx context.Context, id string) (Booking, error) {
	row := d.db.QueryRowContext(ctx, selectBooking+` WHERE id = ?`, id)
	b, err := scanBooking(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Booking{}, ErrNoBooking
	}
	return b, err
}

// Cancel releases the room and keeps the record.
//
// The phone number is the credential, because it is the only thing the booking
// form collected and the customer has it in hand. Passing an empty phone is the
// operator path — the console has already authenticated whoever is asking.
func (d *Desk) Cancel(ctx context.Context, id, phone string) (Booking, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return Booking{}, fmt.Errorf("booking: begin: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, selectBooking+` WHERE id = ?`, id)
	b, err := scanBooking(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Booking{}, ErrNoBooking
	}
	if err != nil {
		return Booking{}, err
	}
	// Wrong number reads as no such booking. Confirming that an id exists to
	// somebody who cannot produce its phone number turns a guessed id into a
	// disclosure that the studio has a customer.
	if phone != "" && b.Phone != phone {
		return Booking{}, ErrNoBooking
	}
	if b.Status == "cancelled" {
		return b, nil
	}

	at := d.now().UTC().Truncate(time.Second)
	if _, err := tx.ExecContext(ctx,
		`UPDATE bookings SET status = 'cancelled', cancelled_at = ? WHERE id = ?`,
		at.Unix(), id); err != nil {
		return Booking{}, fmt.Errorf("booking: cancel: %w", err)
	}
	// Frees the room. The booking row stays, so a dispute about a slot that was
	// taken and given back is still answerable.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM booking_slots WHERE booking_id = ?`, id); err != nil {
		return Booking{}, fmt.Errorf("booking: release: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Booking{}, fmt.Errorf("booking: commit: %w", err)
	}

	b.Status = "cancelled"
	b.CancelledAt = at
	return b, nil
}

// Day lists every booking touching a local day, for the operator's console.
// Cancelled ones are included: an operator looking at a quiet afternoon needs to
// see that it was busy and emptied.
func (d *Desk) Day(ctx context.Context, day time.Time) ([]Booking, error) {
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, WIB)
	to := from.AddDate(0, 0, 1)

	rows, err := d.db.QueryContext(ctx,
		selectBooking+` WHERE starts_at >= ? AND starts_at < ? ORDER BY starts_at, id`,
		from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("booking: day: %w", err)
	}
	defer rows.Close()

	out := make([]Booking, 0, 32)
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ByPhone lists a customer's own bookings, newest first.
func (d *Desk) ByPhone(ctx context.Context, phone string, limit int) ([]Booking, error) {
	rows, err := d.db.QueryContext(ctx,
		selectBooking+` WHERE phone = ? ORDER BY starts_at DESC LIMIT ?`, phone, limit)
	if err != nil {
		return nil, fmt.Errorf("booking: by phone: %w", err)
	}
	defer rows.Close()

	out := make([]Booking, 0, limit)
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

const selectBooking = `
	SELECT id, resource_id, service_id, COALESCE(user_id, ''),
	       starts_at, ends_at, headcount,
	       name, phone, email, notes, status, COALESCE(gcal_event_id, ''),
	       created_at, cancelled_at
	FROM bookings`

// scanner is what Query rows and QueryRow both satisfy, so one scan helper
// serves the list and the single read.
type scanner interface{ Scan(dest ...any) error }

func scanBooking(s scanner) (Booking, error) {
	var b Booking
	var starts, ends, created int64
	var cancelled sql.NullInt64
	if err := s.Scan(&b.ID, &b.ResourceID, &b.ServiceID, &b.UserID,
		&starts, &ends, &b.Headcount,
		&b.Name, &b.Phone, &b.Email, &b.Notes, &b.Status, &b.GCalEventID,
		&created, &cancelled); err != nil {
		return Booking{}, err
	}
	b.StartsAt = time.Unix(starts, 0).UTC()
	b.EndsAt = time.Unix(ends, 0).UTC()
	b.CreatedAt = time.Unix(created, 0).UTC()
	if cancelled.Valid {
		b.CancelledAt = time.Unix(cancelled.Int64, 0).UTC()
	}
	return b, nil
}

func scanService(s scanner) (Service, error) {
	var svc Service
	var perPerson int
	if err := s.Scan(&svc.ID, &svc.ResourceID, &svc.Name, &svc.ServiceLine, &svc.Description,
		&svc.PriceIDR, &perPerson,
		&svc.DurationMinutes, &svc.BufferMinutes,
		&svc.HeadcountMin, &svc.HeadcountMax, &svc.OrderIndex); err != nil {
		return Service{}, err
	}
	svc.PricePerPerson = perPerson != 0
	return svc, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any supported platform; if it does, the
		// process has no business minting identifiers.
		panic("booking: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
