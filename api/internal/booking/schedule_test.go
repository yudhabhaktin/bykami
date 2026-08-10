package booking

import (
	"context"
	"testing"
	"time"
)

// starts renders a day's availability as studio-local HH:MM, which is the only
// form these expectations are readable in.
func starts(slots []Slot) []string {
	out := make([]string, 0, len(slots))
	for _, s := range slots {
		out = append(out, s.StartsAt.In(WIB).Format("15:04"))
	}
	return out
}

func day(d *Desk, t *testing.T, service string, at time.Time) []Slot {
	t.Helper()
	from := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, WIB)
	slots, err := d.Availability(context.Background(), service, from, from.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	return slots
}

func TestAvailabilityReproducesTheOldBookingPage(t *testing.T) {
	d, _ := newDesk(t)

	// Twenty-two slots on a Wednesday: a 09:00-21:00 day on a half-hour grid is
	// twenty-four starts for a thirty-minute session, less Dzuhur and Maghrib.
	// This is the count the studio's own page served, and the two absences are
	// the two prayer breaks it had configured.
	got := starts(day(d, t, "photobox-y2k", wib(2026, 8, 12, 0, 0)))
	want := []string{
		"09:00", "09:30", "10:00", "10:30", "11:00", "11:30",
		"12:30", "13:00", "13:30", "14:00", "14:30", "15:00",
		"15:30", "16:00", "16:30", "17:00",
		"18:00", "18:30", "19:00", "19:30", "20:00", "20:30",
	}
	if len(got) != len(want) {
		t.Fatalf("Wednesday offered %d slots %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("slot %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestFridayMovesTheMiddayBreakAndKeepsTheCount(t *testing.T) {
	d, _ := newDesk(t)

	got := starts(day(d, t, "photobox-y2k", wib(2026, 8, 14, 0, 0)))
	if len(got) != 22 {
		t.Fatalf("Friday offered %d slots %v, want 22", len(got), got)
	}

	seen := map[string]bool{}
	for _, s := range got {
		seen[s] = true
	}
	// Jumatan takes 11:30 and gives 12:00 back. A day that lost both would be a
	// break rule applied twice; a day that lost neither would be one applied to
	// the wrong weekday.
	if seen["11:30"] {
		t.Error("11:30 was for sale on a Friday — Jumatan is not blocked")
	}
	if !seen["12:00"] {
		t.Error("12:00 was not for sale on a Friday — the midday break did not move")
	}
	if seen["17:30"] {
		t.Error("17:30 was for sale — Maghrib is not blocked")
	}
}

func TestAvailabilityStartsAfterTheMinimumNotice(t *testing.T) {
	d, _ := newDesk(t)

	// The reading this suite's clock was taken from: at 19:53 the studio's own
	// page offered exactly one slot for the rest of that Monday, 20:30. Thirty
	// minutes' notice rounded up to the grid lands there — 20:00 is inside the
	// notice and 20:30 is the next start.
	got := starts(day(d, t, "photobox-y2k", wib(2026, 8, 10, 0, 0)))
	if len(got) != 1 || got[0] != "20:30" {
		t.Errorf("the rest of the evening offered %v, want [20:30]", got)
	}
}

func TestAvailabilityStopsAtTheEndOfTheWindow(t *testing.T) {
	d, _ := newDesk(t)

	// Asking for next year returns what is bookable, not a year of slots the
	// booking call would then refuse.
	from := wib(2026, 8, 12, 0, 0)
	slots, err := d.Availability(context.Background(), "photobox-y2k", from, from.AddDate(1, 0, 0))
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if len(slots) == 0 {
		t.Fatal("a year-long request returned nothing")
	}
	last := slots[len(slots)-1].StartsAt
	limit := testNow.Add(defaultWindow)
	if last.After(limit) {
		t.Errorf("last slot %s is past the window end %s", last, limit)
	}
	// And the whole window is actually offered, not just the first day.
	if first := slots[0].StartsAt; last.Sub(first) < 20*24*time.Hour {
		t.Errorf("the window only spanned %v", last.Sub(first))
	}
}

func TestALongSessionOnlyStartsWhereItFits(t *testing.T) {
	d, _ := newDesk(t)

	// A three-hour shoot has to clear both prayer breaks and finish by closing,
	// which leaves seven starts in a twelve-hour day. Anything that reports more
	// is offering a session that runs through Maghrib or past 21:00.
	got := starts(day(d, t, "photographer-3h", wib(2026, 8, 12, 0, 0)))
	want := []string{"09:00", "12:30", "13:00", "13:30", "14:00", "14:30", "18:00"}
	if len(got) != len(want) {
		t.Fatalf("three-hour shoots could start at %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("start %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestABookingLeavesTheGrid(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	before := len(day(d, t, "photobox-y2k", wib(2026, 8, 12, 0, 0)))
	if _, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2)); err != nil {
		t.Fatalf("book: %v", err)
	}
	got := starts(day(d, t, "photobox-y2k", wib(2026, 8, 12, 0, 0)))
	if len(got) != before-1 {
		t.Errorf("after one booking the day offered %d slots, want %d", len(got), before-1)
	}
	for _, s := range got {
		if s == "14:00" {
			t.Error("14:00 is still for sale after being booked")
		}
	}

	// And the other resource is untouched, because they are separate rooms.
	other := starts(day(d, t, "self-mini", wib(2026, 8, 12, 0, 0)))
	found := false
	for _, s := range other {
		if s == "14:00" {
			found = true
		}
	}
	if !found {
		t.Error("booking the photobox took 14:00 off self photo too")
	}
}

func TestWhatTheOwnerBlocksInGoogleCalendarLeavesTheGrid(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	// An hour blocked by hand in the owner's calendar. Not aligned to the grid on
	// purpose: a range from 14:05 takes the 14:00 slot, because the room is
	// occupied for part of it and half a session is not a thing to sell.
	if err := d.ReplaceBusy(ctx, "photobox",
		wib(2026, 8, 12, 0, 0), wib(2026, 8, 13, 0, 0),
		[]Busy{{StartsAt: wib(2026, 8, 12, 14, 5), EndsAt: wib(2026, 8, 12, 15, 0)}}); err != nil {
		t.Fatalf("replace busy: %v", err)
	}

	for _, s := range starts(day(d, t, "photobox-y2k", wib(2026, 8, 12, 0, 0))) {
		if s == "14:00" || s == "14:30" {
			t.Errorf("%s is for sale during a range the owner blocked", s)
		}
	}
	// And it cannot be booked around the back of the availability call either.
	if _, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2)); err == nil {
		t.Error("a slot the owner blocked in Google Calendar was booked anyway")
	}
}

func TestAFailedPollKeepsTheCachedBusyRanges(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	if err := d.ReplaceBusy(ctx, "photobox",
		wib(2026, 8, 12, 0, 0), wib(2026, 8, 13, 0, 0),
		[]Busy{{StartsAt: wib(2026, 8, 12, 14, 0), EndsAt: wib(2026, 8, 12, 15, 0)}}); err != nil {
		t.Fatalf("replace busy: %v", err)
	}
	blocked := len(day(d, t, "photobox-y2k", wib(2026, 8, 12, 0, 0)))

	// Google is down. The cached ranges are the best answer available and must
	// stay in service — the alternative is a studio that shows every slot as free
	// and takes a booking on top of the owner's own appointment.
	if err := d.RecordSyncFailure(ctx, "photobox", "googleapi: 503 backendError"); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	if got := len(day(d, t, "photobox-y2k", wib(2026, 8, 12, 0, 0))); got != blocked {
		t.Errorf("a failed poll changed availability from %d slots to %d", blocked, got)
	}

	// The failure is visible rather than inferred, so an operator can act on it.
	state, err := d.SyncState(ctx)
	if err != nil {
		t.Fatalf("sync state: %v", err)
	}
	var found bool
	for _, s := range state {
		if s.ResourceID != "photobox" {
			continue
		}
		found = true
		if s.Error == "" {
			t.Error("the failed poll left no error for an operator to see")
		}
		if s.FetchedAt.IsZero() {
			t.Error("the last successful fetch time was lost by a failure")
		}
	}
	if !found {
		t.Error("sync state did not mention the photobox")
	}
}

func TestASuccessfulPollClearsTheError(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	if err := d.RecordSyncFailure(ctx, "photobox", "googleapi: 403 notFound"); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	if err := d.ReplaceBusy(ctx, "photobox",
		wib(2026, 8, 12, 0, 0), wib(2026, 8, 13, 0, 0), nil); err != nil {
		t.Fatalf("replace busy: %v", err)
	}
	state, _ := d.SyncState(ctx)
	for _, s := range state {
		if s.ResourceID == "photobox" && s.Error != "" {
			t.Errorf("a successful poll left the old error %q behind", s.Error)
		}
	}
}

func TestReplacingBusyRangesDropsTheOnesTheOwnerDeleted(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	from, to := wib(2026, 8, 12, 0, 0), wib(2026, 8, 13, 0, 0)
	if err := d.ReplaceBusy(ctx, "photobox", from, to,
		[]Busy{{StartsAt: wib(2026, 8, 12, 14, 0), EndsAt: wib(2026, 8, 12, 15, 0)}}); err != nil {
		t.Fatalf("replace busy: %v", err)
	}
	// The owner deletes that appointment. There is no tombstone to sync, so the
	// only way the slot comes back is the set being replaced wholesale.
	if err := d.ReplaceBusy(ctx, "photobox", from, to, nil); err != nil {
		t.Fatalf("replace busy: %v", err)
	}

	var found bool
	for _, s := range starts(day(d, t, "photobox-y2k", from)) {
		if s == "14:00" {
			found = true
		}
	}
	if !found {
		t.Error("14:00 stayed blocked after the owner deleted the appointment")
	}
}

func TestUnmirroredIsTheRetryQueue(t *testing.T) {
	d, _ := newDesk(t)
	ctx := context.Background()

	b, err := d.Book(ctx, request("photobox-y2k", wib(2026, 8, 12, 14, 0), 2))
	if err != nil {
		t.Fatalf("book: %v", err)
	}

	// A booking that has not reached Google yet is confirmed and queued. It is a
	// query rather than a table because the condition is already in the data.
	pending, err := d.Unmirrored(ctx, 10)
	if err != nil {
		t.Fatalf("unmirrored: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != b.ID {
		t.Fatalf("queue held %d bookings, want the one just made", len(pending))
	}

	if err := d.AttachEvent(ctx, b.ID, "evt_123"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	pending, _ = d.Unmirrored(ctx, 10)
	if len(pending) != 0 {
		t.Errorf("a mirrored booking is still queued: %+v", pending)
	}

	// Cancelling it queues the event for removal instead.
	if _, err := d.Cancel(ctx, b.ID, ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	gone, err := d.Cancelled(ctx, 10)
	if err != nil {
		t.Fatalf("cancelled: %v", err)
	}
	if len(gone) != 1 || gone[0].GCalEventID != "evt_123" {
		t.Errorf("cancelled queue held %+v, want the event to delete", gone)
	}
	if err := d.ForgetEvent(ctx, b.ID); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if gone, _ = d.Cancelled(ctx, 10); len(gone) != 0 {
		t.Errorf("a deleted event is still queued: %+v", gone)
	}
}

func TestAvailabilityRefusesAServiceThatDoesNotExist(t *testing.T) {
	d, _ := newDesk(t)
	from := wib(2026, 8, 12, 0, 0)
	if _, err := d.Availability(context.Background(), "massage", from, from.AddDate(0, 0, 1)); err == nil {
		t.Error("availability answered for a service the studio does not sell")
	}
}
