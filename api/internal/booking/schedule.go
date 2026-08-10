package booking

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// hours is one weekday's opening window, in minutes past local midnight.
type hours struct{ opens, closes int }

// schedule is everything that decides whether a given half hour can be sold,
// resolved once for a date range so a month of availability is a handful of
// queries rather than one per day.
type schedule struct {
	hours map[time.Weekday]hours
	// blocked is keyed by the unix second of a grid point. A point is in here if
	// anything at all stands in the way: an existing booking, a prayer break, a
	// blackout, or a range the owner filled in by hand in Google Calendar. The
	// reasons are deliberately collapsed — a customer is told a time is
	// unavailable and never why, because "the owner has a dentist appointment"
	// is not the studio's to publish.
	blocked map[int64]bool
}

// open reports whether a session of span starting at start fits inside that
// day's opening hours. A session that would run past closing is not offered,
// which is why the last photobox slot is 20:30 and not 20:50.
func (s schedule) open(start time.Time, span time.Duration) bool {
	local := start.In(WIB)
	h, ok := s.hours[local.Weekday()]
	if !ok {
		return false
	}
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, WIB)
	from := int(local.Sub(midnight) / time.Minute)
	to := from + int(span/time.Minute)
	return from >= h.opens && to <= h.closes
}

// Availability lists the times a service can be started, between from and to.
//
// The range is clamped to what is actually sellable — nothing inside the minimum
// notice, nothing past the end of the booking window — so a client asking for
// next year gets an empty list rather than a year of slots it will be refused on.
func (d *Desk) Availability(ctx context.Context, serviceID string, from, to time.Time) ([]Slot, error) {
	svc, err := d.Service(ctx, serviceID)
	if err != nil {
		return nil, err
	}

	now := d.now().UTC()
	grid := slotMinutes * time.Minute

	// Round the earliest offer up to the grid: with 30 minutes' notice at 19:53
	// the first slot is 20:30, which is exactly what the old booking page did.
	earliest := now.Add(d.notice).Truncate(grid)
	if earliest.Before(now.Add(d.notice)) {
		earliest = earliest.Add(grid)
	}
	if from.Before(earliest) {
		from = earliest
	}
	latest := now.Add(d.window)
	if to.After(latest) {
		to = latest
	}
	if !to.After(from) {
		return []Slot{}, nil
	}

	span := svc.span()
	// The schedule has to reach past `to`, because a session starting inside the
	// range finishes outside it and every half hour it occupies must be known.
	sched, err := d.schedule(ctx, svc.ResourceID, from, to.Add(span))
	if err != nil {
		return nil, err
	}

	out := make([]Slot, 0, 64)
	day := startOfDay(from)
	for !day.After(to) {
		h, ok := sched.hours[day.In(WIB).Weekday()]
		if !ok {
			day = day.AddDate(0, 0, 1)
			continue
		}
		for m := h.opens; m <= h.closes-int(span/time.Minute); m += slotMinutes {
			start := day.Add(time.Duration(m) * time.Minute)
			if start.Before(from) || !start.Before(to) {
				continue
			}
			if free(sched, start, span) {
				out = append(out, Slot{
					StartsAt: start.UTC(),
					EndsAt:   start.Add(time.Duration(svc.DurationMinutes) * time.Minute).UTC(),
				})
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return out, nil
}

func free(s schedule, start time.Time, span time.Duration) bool {
	for _, p := range gridPoints(start, span) {
		if s.blocked[p.Unix()] {
			return false
		}
	}
	return true
}

// schedule resolves opening hours and everything blocking the range.
func (d *Desk) schedule(ctx context.Context, resourceID string, from, to time.Time) (schedule, error) {
	s := schedule{
		hours:   make(map[time.Weekday]hours, 7),
		blocked: make(map[int64]bool, 256),
	}

	rows, err := d.db.QueryContext(ctx, `SELECT weekday, opens_at, closes_at FROM booking_hours`)
	if err != nil {
		return schedule{}, fmt.Errorf("booking: hours: %w", err)
	}
	for rows.Next() {
		var wd, opens, closes int
		if err := rows.Scan(&wd, &opens, &closes); err != nil {
			rows.Close()
			return schedule{}, fmt.Errorf("booking: hours: %w", err)
		}
		s.hours[time.Weekday(wd)] = hours{opens: opens, closes: closes}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return schedule{}, fmt.Errorf("booking: hours: %w", err)
	}

	// Recurring breaks, expanded over the range. Held per weekday, so Friday's
	// midday break lands at 11:30 and the rest of the week's at 12:00 without
	// this loop knowing anything about either.
	breaks, err := d.breaks(ctx)
	if err != nil {
		return schedule{}, err
	}
	for day := startOfDay(from); !day.After(to); day = day.AddDate(0, 0, 1) {
		wd := day.In(WIB).Weekday()
		for _, b := range breaks {
			if b.weekday != nil && *b.weekday != wd {
				continue
			}
			markRange(s.blocked,
				day.Add(time.Duration(b.starts)*time.Minute),
				day.Add(time.Duration(b.ends)*time.Minute))
		}
	}

	// One-off closures. A NULL resource_id closes everything, which is what
	// "closed today" means.
	if err := d.eachRange(ctx, s.blocked,
		`SELECT starts_at, ends_at FROM booking_blackouts
		 WHERE (resource_id IS NULL OR resource_id = ?) AND ends_at > ? AND starts_at < ?`,
		resourceID, from.Unix(), to.Unix()); err != nil {
		return schedule{}, fmt.Errorf("booking: blackouts: %w", err)
	}

	// What the owner blocked by hand in Google Calendar, as of the last poll that
	// worked. Stale rather than absent on purpose — see the migration.
	if err := d.eachRange(ctx, s.blocked,
		`SELECT starts_at, ends_at FROM booking_calendar_busy
		 WHERE resource_id = ? AND ends_at > ? AND starts_at < ?`,
		resourceID, from.Unix(), to.Unix()); err != nil {
		return schedule{}, fmt.Errorf("booking: calendar busy: %w", err)
	}

	// Already sold. These are grid points, not ranges.
	taken, err := d.db.QueryContext(ctx,
		`SELECT starts_at FROM booking_slots
		 WHERE resource_id = ? AND starts_at >= ? AND starts_at < ?`,
		resourceID, from.Unix(), to.Unix())
	if err != nil {
		return schedule{}, fmt.Errorf("booking: taken: %w", err)
	}
	defer taken.Close()
	for taken.Next() {
		var ts int64
		if err := taken.Scan(&ts); err != nil {
			return schedule{}, fmt.Errorf("booking: taken: %w", err)
		}
		s.blocked[ts] = true
	}
	return s, taken.Err()
}

type recurringBreak struct {
	weekday *time.Weekday
	starts  int
	ends    int
}

func (d *Desk) breaks(ctx context.Context) ([]recurringBreak, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT weekday, starts_at, ends_at FROM booking_breaks`)
	if err != nil {
		return nil, fmt.Errorf("booking: breaks: %w", err)
	}
	defer rows.Close()

	var out []recurringBreak
	for rows.Next() {
		var wd sql.NullInt64
		var b recurringBreak
		if err := rows.Scan(&wd, &b.starts, &b.ends); err != nil {
			return nil, fmt.Errorf("booking: breaks: %w", err)
		}
		if wd.Valid {
			day := time.Weekday(wd.Int64)
			b.weekday = &day
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// eachRange runs a query returning (starts_at, ends_at) unix pairs and marks
// every grid point they touch.
func (d *Desk) eachRange(ctx context.Context, blocked map[int64]bool, query string, args ...any) error {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var from, to int64
		if err := rows.Scan(&from, &to); err != nil {
			return err
		}
		markRange(blocked, time.Unix(from, 0).UTC(), time.Unix(to, 0).UTC())
	}
	return rows.Err()
}

// markRange blocks every grid point overlapping [from, to).
//
// Overlapping, not contained: a busy range from 09:05 to 09:15 sits inside the
// 09:00 slot and takes it, because the room is occupied for part of it and a
// customer cannot use the other part.
func markRange(blocked map[int64]bool, from, to time.Time) {
	grid := slotMinutes * time.Minute
	for p := from.Truncate(grid); p.Before(to); p = p.Add(grid) {
		blocked[p.Unix()] = true
	}
}

// gridPoints lists the half hours a session starting at start occupies.
func gridPoints(start time.Time, span time.Duration) []time.Time {
	grid := slotMinutes * time.Minute
	n := int(span / grid)
	if n < 1 {
		n = 1
	}
	out := make([]time.Time, 0, n)
	for i := range n {
		out = append(out, start.Add(time.Duration(i)*grid))
	}
	return out
}

// startOfDay is local midnight, which is where a day's grid begins. Computed in
// WIB and returned as an instant, so arithmetic on it stays in UTC.
func startOfDay(t time.Time) time.Time {
	l := t.In(WIB)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, WIB)
}
