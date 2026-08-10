package booking

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCalendar stands in for the owner's Google Calendar. An interface rather
// than an httptest server here on purpose: what these tests are about is what the
// worker does when the calendar answers, refuses, or has already forgotten — none
// of which is a question about JSON. The wire format is internal/gcal's to prove.
type fakeCalendar struct {
	mu sync.Mutex

	busy map[string][]Busy

	inserted []insertedEvent
	deleted  []string

	freeBusyErr error
	insertErr   error
	deleteErr   error
	// brokenCalendar fails freeBusy for one calendar id only, which is what an
	// unshared calendar looks like next to two working ones.
	brokenCalendar string
}

type insertedEvent struct {
	calendarID string
	event      Event
}

func (f *fakeCalendar) FreeBusy(_ context.Context, calendarID string, _, _ time.Time) ([]Busy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.brokenCalendar != "" && calendarID == f.brokenCalendar {
		return nil, errors.New("notFound — check it is shared")
	}
	if f.freeBusyErr != nil {
		return nil, f.freeBusyErr
	}
	return f.busy[calendarID], nil
}

func (f *fakeCalendar) Insert(_ context.Context, calendarID string, ev Event) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return "", f.insertErr
	}
	f.inserted = append(f.inserted, insertedEvent{calendarID: calendarID, event: ev})
	return "evt_1", nil
}

func (f *fakeCalendar) Delete(_ context.Context, _, eventID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, eventID)
	return nil
}

func (f *fakeCalendar) events() []insertedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]insertedEvent(nil), f.inserted...)
}

// connect gives the seeded resources calendars, which the seed deliberately
// leaves empty — an unconnected resource is the state of a fresh deployment.
func connect(t *testing.T, db *sql.DB) {
	t.Helper()
	for id, cal := range map[string]string{
		"photobox":     "photobox@group.calendar.google.com",
		"self-photo":   "self@group.calendar.google.com",
		"photographer": "shoot@group.calendar.google.com",
	} {
		if _, err := db.Exec(
			`UPDATE booking_resources SET google_calendar_id = ? WHERE id = ?`, cal, id); err != nil {
			t.Fatalf("connect %s: %v", id, err)
		}
	}
}

func newWorker(t *testing.T, d *Desk, cal Calendar) *Worker {
	t.Helper()
	// Discarded, so an expected failure path does not bury a real one in output.
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	w := NewWorker(d, cal, log, time.Minute, "Jajag, Banyuwangi")
	if w == nil {
		t.Fatal("NewWorker returned nil for a configured calendar")
	}
	return w
}

func TestNoCalendarMeansNoWorker(t *testing.T) {
	d, _ := newDesk(t)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	// Nil, the way cmd/bykami already treats the Instagram mirror: a deployment
	// with no credentials runs without this, it does not fail to start.
	if w := NewWorker(d, nil, log, time.Minute, ""); w != nil {
		t.Error("a worker was built with no calendar to talk to")
	}
}

func TestSyncPullsWhatTheOwnerBlockedIntoAvailability(t *testing.T) {
	d, db := newDesk(t)
	connect(t, db)
	ctx := context.Background()

	at := wib(2026, 8, 12, 14, 0)
	cal := &fakeCalendar{busy: map[string][]Busy{
		"photobox@group.calendar.google.com": {{StartsAt: at, EndsAt: at.Add(time.Hour)}},
	}}

	if err := newWorker(t, d, cal).Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	for _, s := range starts(day(d, t, "photobox-y2k", at)) {
		if s == "14:00" || s == "14:30" {
			t.Errorf("%s is still for sale after the owner blocked it", s)
		}
	}
	// And only that resource: the calendars are separate.
	found := false
	for _, s := range starts(day(d, t, "self-mini", at)) {
		if s == "14:00" {
			found = true
		}
	}
	if !found {
		t.Error("a busy range on the photobox calendar blocked self photo")
	}
}

func TestSyncWritesTheBookingIntoTheOwnersCalendar(t *testing.T) {
	d, db := newDesk(t)
	connect(t, db)
	ctx := context.Background()

	b, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 3))
	if err != nil {
		t.Fatalf("book: %v", err)
	}

	cal := &fakeCalendar{}
	if err := newWorker(t, d, cal).Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	events := cal.events()
	if len(events) != 1 {
		t.Fatalf("wrote %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.calendarID != "photobox@group.calendar.google.com" {
		t.Errorf("wrote to %q, want the photobox calendar", ev.calendarID)
	}

	// The event spans the reservation, not the session. A photobox booking is ten
	// minutes of a half-hour slot, and an event ending at 14:10 tells the owner
	// 14:10 is free when the system is holding it until 14:30.
	if got := ev.event.EndsAt.Sub(ev.event.StartsAt); got != 30*time.Minute {
		t.Errorf("event ran %v, want the 30m reservation", got)
	}
	for _, want := range []string{"Rina", "+6281234567890", "3 orang", b.ID} {
		if !strings.Contains(ev.event.Description, want) {
			t.Errorf("event description is missing %q:\n%s", want, ev.event.Description)
		}
	}
	if ev.event.Location == "" {
		t.Error("event has no location for the owner to navigate from")
	}

	// Mirrored once. A second pass must not write a duplicate into the calendar
	// the owner actually works from.
	if err := newWorker(t, d, cal).Sync(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := len(cal.events()); got != 1 {
		t.Errorf("a second pass wrote %d events in total, want 1", got)
	}
}

func TestACalendarThatRefusesTheEventKeepsTheBooking(t *testing.T) {
	d, db := newDesk(t)
	connect(t, db)
	ctx := context.Background()

	b, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2))
	if err != nil {
		t.Fatalf("book: %v", err)
	}

	cal := &fakeCalendar{insertErr: errors.New("googleapi: 503 backendError")}
	if err := newWorker(t, d, cal).Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// The customer has already been told this is confirmed. A calendar that will
	// not take it is a thing to retry, never a reason to undo a sale.
	got, err := d.Get(ctx, b.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "confirmed" {
		t.Errorf("status = %q, want confirmed", got.Status)
	}
	if got.GCalEventID != "" {
		t.Errorf("event id = %q, want empty so it stays queued", got.GCalEventID)
	}
	pending, _ := d.Unmirrored(ctx, 10)
	if len(pending) != 1 {
		t.Errorf("%d bookings queued for retry, want 1", len(pending))
	}

	// And the slot is still held, so nobody else can take it while Google is down.
	if _, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2)); !errors.Is(err, ErrSlotTaken) {
		t.Errorf("the slot came free while the mirror was failing: %v", err)
	}

	// Google comes back.
	cal.mu.Lock()
	cal.insertErr = nil
	cal.mu.Unlock()
	if err := newWorker(t, d, cal).Sync(ctx); err != nil {
		t.Fatalf("retry sync: %v", err)
	}
	if got, _ = d.Get(ctx, b.ID); got.GCalEventID == "" {
		t.Error("the retry did not mirror the booking")
	}
}

func TestSyncTakesCancelledBookingsBackOutOfTheCalendar(t *testing.T) {
	d, db := newDesk(t)
	connect(t, db)
	ctx := context.Background()

	b, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2))
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	cal := &fakeCalendar{}
	w := newWorker(t, d, cal)
	if err := w.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := d.Cancel(ctx, b.ID, ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := w.Sync(ctx); err != nil {
		t.Fatalf("sync after cancel: %v", err)
	}

	cal.mu.Lock()
	deleted := append([]string(nil), cal.deleted...)
	cal.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "evt_1" {
		t.Fatalf("deleted %v, want [evt_1]", deleted)
	}
	// The link is dropped so the queue drains rather than retrying forever.
	if got, _ := d.Get(ctx, b.ID); got.GCalEventID != "" {
		t.Errorf("event id = %q, want empty once the event is gone", got.GCalEventID)
	}
}

func TestAnEventTheOwnerAlreadyDeletedIsForgotten(t *testing.T) {
	d, db := newDesk(t)
	connect(t, db)
	ctx := context.Background()

	b, _ := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2))
	cal := &fakeCalendar{}
	w := newWorker(t, d, cal)
	w.Sync(ctx)
	d.Cancel(ctx, b.ID, "")

	// The owner deleted it by hand before the worker got there. That is the state
	// the deletion was trying to reach, so it counts as done — anything else
	// retries on every tick for as long as the row exists.
	cal.mu.Lock()
	cal.deleteErr = ErrEventGone
	cal.mu.Unlock()

	if err := w.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got, _ := d.Get(ctx, b.ID); got.GCalEventID != "" {
		t.Errorf("event id = %q, want empty", got.GCalEventID)
	}
}

func TestOneUnsharedCalendarDoesNotStopTheOthers(t *testing.T) {
	d, db := newDesk(t)
	connect(t, db)
	ctx := context.Background()

	at := wib(2026, 8, 12, 14, 0)
	cal := &fakeCalendar{
		brokenCalendar: "photobox@group.calendar.google.com",
		busy: map[string][]Busy{
			"self@group.calendar.google.com": {{StartsAt: at, EndsAt: at.Add(30 * time.Minute)}},
		},
	}
	if err := newWorker(t, d, cal).Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// The working calendar synced. One resource nobody has shared yet must not
	// cost the studio availability on the other two.
	blocked := true
	for _, s := range starts(day(d, t, "self-mini", at)) {
		if s == "14:00" {
			blocked = false
		}
	}
	if !blocked {
		t.Error("the good calendar did not sync alongside the broken one")
	}

	// And the failure is recorded against the resource it belongs to.
	state, err := d.SyncState(ctx)
	if err != nil {
		t.Fatalf("sync state: %v", err)
	}
	for _, s := range state {
		switch s.ResourceID {
		case "photobox":
			if s.Error == "" {
				t.Error("the unshared calendar recorded no error")
			}
		case "self-photo":
			if s.Error != "" {
				t.Errorf("the working calendar recorded an error: %q", s.Error)
			}
		}
	}
}

func TestSyncSkipsResourcesWithNoCalendar(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	// Nothing connected, which is how the box ships. The booking still has to be
	// written and still has to hold its slot; it simply is not mirrored anywhere.
	if _, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2)); err != nil {
		t.Fatalf("book: %v", err)
	}
	cal := &fakeCalendar{}
	if err := newWorker(t, d, cal).Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := len(cal.events()); got != 0 {
		t.Errorf("wrote %d events for an unconnected resource, want 0", got)
	}
}
