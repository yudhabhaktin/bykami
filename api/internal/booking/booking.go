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
	// ErrChatOnly means the package is agreed over WhatsApp and has no slots to
	// offer. Returned by both Availability and Book rather than only by the page,
	// because the page is not the only thing that can post to the API.
	ErrChatOnly = errors.New("booking: that package is arranged by chat")
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
	// ErrBackdropRequired means the package offers a choice of wall and the
	// request made none. Refused rather than defaulted to the first one: a
	// background nobody picked is one the operator hangs and the customer did not
	// ask for, which is the failure this whole feature exists to stop.
	ErrBackdropRequired = errors.New("booking: that package needs a background chosen")
	// ErrBackdropUnknown means the wall is not one this package is shot against —
	// including the case of naming one at all for a package that offers none.
	ErrBackdropUnknown = errors.New("booking: that background is not offered with that package")
	// ErrBadCalendarID means the value is not the shape of a Google Calendar id.
	ErrBadCalendarID = errors.New("booking: not a calendar id")
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

	// BookingMode is ModeWeb or ModeChat. See ErrChatOnly.
	BookingMode string

	// Backdrops is the walls this package may be shot against, in the order the
	// package presents them. Empty means the question does not arise — a photobox
	// booth's backdrop is built into the booth — and a customer is then asked
	// nothing rather than being shown a picker with one entry.
	Backdrops []Backdrop
}

// Backdrop is a wall a session can be shot against: a paper roll the operator
// hangs, or one of the patterned walls the motif room is dressed in.
type Backdrop struct {
	ID   string
	Name string
}

// How a package is sold. A photographer session is quoted rather than booked, so
// it is listed and priced but offers no slots — see 0007_chat_only_services.
const (
	ModeWeb  = "web"
	ModeChat = "chat"
)

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

	// BackdropID is the wall to hang before this session, and Backdrop is its
	// name. Both empty where the package offers no choice, which is most of the
	// catalogue and every booking taken before backdrops existed.
	//
	// The name is read through the join rather than copied onto the row, unlike
	// Name and Phone above: those record who turned up and must not change when a
	// customer corrects their spelling, whereas this records which roll to hang
	// and the roll is the same roll whatever it is called this month.
	BackdropID string
	Backdrop   string

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
	// BackdropID is required when the package offers a choice and refused when it
	// does not. See ErrBackdropRequired and ErrBackdropUnknown.
	BackdropID string
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
	out, err := d.serviceRows(ctx)
	if err != nil {
		return nil, err
	}

	// After serviceRows has returned, and deliberately not inside its loop. The
	// pool is capped at one connection — see store.Open — so a query issued while
	// another statement's rows are still open waits for a connection that only
	// closing those rows can free.
	//
	// One query for the whole catalogue rather than one per package: the list is
	// a dozen rows and the join is a dozen more, the page reads it on every visit,
	// and a query per service is the shape that gets slow quietly.
	walls, err := d.backdrops(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Backdrops = walls[out[i].ID]
	}
	return out, nil
}

func (d *Desk) serviceRows(ctx context.Context) ([]Service, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT s.id, s.resource_id, s.name, s.service_line, s.description,
		        s.price_idr, s.price_per_person,
		        s.duration_minutes, s.buffer_minutes,
		        s.headcount_min, s.headcount_max, s.order_index, s.booking_mode
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

// backdrops reads what each package may be shot against. An empty serviceID asks
// about the whole catalogue.
//
// Withdrawn walls are filtered here rather than deleted, so a package stops
// offering one the moment it is deactivated while the bookings that already
// chose it still read back a name.
func (d *Desk) backdrops(ctx context.Context, serviceID string) (map[string][]Backdrop, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT sb.service_id, k.id, k.name
		 FROM booking_service_backdrops sb
		 JOIN booking_backdrops k ON k.id = sb.backdrop_id
		 WHERE k.active = 1 AND (? = '' OR sb.service_id = ?)
		 ORDER BY sb.service_id, sb.order_index, k.id`, serviceID, serviceID)
	if err != nil {
		return nil, fmt.Errorf("booking: backdrops: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]Backdrop, 16)
	for rows.Next() {
		var service string
		var b Backdrop
		if err := rows.Scan(&service, &b.ID, &b.Name); err != nil {
			return nil, fmt.Errorf("booking: backdrops: %w", err)
		}
		out[service] = append(out[service], b)
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
		        s.headcount_min, s.headcount_max, s.order_index, s.booking_mode
		 FROM booking_services s WHERE s.id = ?`, id)
	s, err := scanService(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Service{}, ErrNoService
	}
	if err != nil {
		return Service{}, err
	}

	walls, err := d.backdrops(ctx, id)
	if err != nil {
		return Service{}, err
	}
	s.Backdrops = walls[id]
	return s, nil
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
	// Before anything else is checked, because a chat package has no slot to
	// take and no agreed price to take it against.
	if svc.BookingMode == ModeChat {
		return Booking{}, fmt.Errorf("%w: %s", ErrChatOnly, svc.Name)
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

	// Checked here rather than trusted from the client for the same reason the
	// schedule is: the request names a wall, and a stale page still offering a
	// backdrop the studio has withdrawn would otherwise book a session against
	// paper that is no longer in the building.
	backdrop, err := chooseBackdrop(svc, req.BackdropID)
	if err != nil {
		return Booking{}, err
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
		EndsAt:     start.Add(time.Duration(svc.DurationMinutes) * time.Minute),
		Headcount:  req.Headcount,
		Name:       req.Name,
		Phone:      req.Phone,
		Email:      req.Email,
		Notes:      req.Notes,
		BackdropID: backdrop.ID,
		Backdrop:   backdrop.Name,
		Status:     "confirmed",
		CreatedAt:  now.Truncate(time.Second),
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return Booking{}, fmt.Errorf("booking: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO bookings
		   (id, resource_id, service_id, user_id, starts_at, ends_at, headcount,
		    name, phone, email, notes, backdrop_id, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'confirmed', ?)`,
		b.ID, b.ResourceID, b.ServiceID, nullIfEmpty(b.UserID),
		b.StartsAt.Unix(), b.EndsAt.Unix(), b.Headcount,
		b.Name, b.Phone, b.Email, b.Notes, nullIfEmpty(b.BackdropID),
		b.CreatedAt.Unix()); err != nil {
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
	row := d.db.QueryRowContext(ctx, selectBooking+` WHERE b.id = ?`, id)
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

	row := tx.QueryRowContext(ctx, selectBooking+` WHERE b.id = ?`, id)
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
		selectBooking+` WHERE b.starts_at >= ? AND b.starts_at < ? ORDER BY b.starts_at, b.id`,
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
		selectBooking+` WHERE b.phone = ? ORDER BY b.starts_at DESC LIMIT ?`, phone, limit)
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

// chooseBackdrop matches a requested wall against what the package offers.
//
// Three outcomes rather than two, because "you have to pick one" and "that is
// not one of them" are different mistakes and the customer can only fix the
// first. A package with nothing to offer refuses a request that names a wall
// anyway: it means the client is working from a catalogue this server does not
// recognise, and silently dropping the value would put a backdrop on the
// customer's screen that never reached the operator's.
func chooseBackdrop(svc Service, id string) (Backdrop, error) {
	if len(svc.Backdrops) == 0 {
		if id != "" {
			return Backdrop{}, fmt.Errorf("%w: %s", ErrBackdropUnknown, svc.Name)
		}
		return Backdrop{}, nil
	}
	if id == "" {
		return Backdrop{}, fmt.Errorf("%w: %s", ErrBackdropRequired, svc.Name)
	}
	for _, b := range svc.Backdrops {
		if b.ID == id {
			return b, nil
		}
	}
	return Backdrop{}, fmt.Errorf("%w: %s", ErrBackdropUnknown, svc.Name)
}

// The alias and the left join are what let a booking carry the name of its wall
// without a second round trip. Every column is qualified because `id`, `name`
// and `status` all exist on both sides of that join.
const selectBooking = `
	SELECT b.id, b.resource_id, b.service_id, COALESCE(b.user_id, ''),
	       b.starts_at, b.ends_at, b.headcount,
	       b.name, b.phone, b.email, b.notes,
	       COALESCE(b.backdrop_id, ''), COALESCE(k.name, ''),
	       b.status, COALESCE(b.gcal_event_id, ''),
	       b.created_at, b.cancelled_at
	FROM bookings b
	LEFT JOIN booking_backdrops k ON k.id = b.backdrop_id`

// scanner is what Query rows and QueryRow both satisfy, so one scan helper
// serves the list and the single read.
type scanner interface{ Scan(dest ...any) error }

func scanBooking(s scanner) (Booking, error) {
	var b Booking
	var starts, ends, created int64
	var cancelled sql.NullInt64
	if err := s.Scan(&b.ID, &b.ResourceID, &b.ServiceID, &b.UserID,
		&starts, &ends, &b.Headcount,
		&b.Name, &b.Phone, &b.Email, &b.Notes,
		&b.BackdropID, &b.Backdrop,
		&b.Status, &b.GCalEventID,
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
		&svc.HeadcountMin, &svc.HeadcountMax, &svc.OrderIndex, &svc.BookingMode); err != nil {
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
