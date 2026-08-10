package booking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Busy is a range the owner's calendar says a resource is unavailable.
type Busy struct {
	StartsAt time.Time
	EndsAt   time.Time
}

// Sync is what the last calendar poll managed, per resource.
type Sync struct {
	ResourceID string
	CalendarID string
	FetchedAt  time.Time
	Error      string
}

// Resources lists what exists, connected calendar or not. The calendar worker
// polls from this; the console reads it to show what is connected.
func (d *Desk) Resources(ctx context.Context) ([]Resource, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, name, google_calendar_id FROM booking_resources
		 WHERE active = 1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("booking: resources: %w", err)
	}
	defer rows.Close()

	out := make([]Resource, 0, 8)
	for rows.Next() {
		var r Resource
		if err := rows.Scan(&r.ID, &r.Name, &r.GoogleCalendarID); err != nil {
			return nil, fmt.Errorf("booking: resources: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceBusy swaps in a fresh set of busy ranges for one resource.
//
// Wholesale rather than incremental, in one transaction. A range the owner
// deleted from their calendar has no tombstone to sync, so the only way to stop
// blocking it is to stop holding it — and doing that as delete-then-insert
// inside a transaction means availability never observes a resource with no
// busy ranges at all, which would briefly offer times that are taken.
func (d *Desk) ReplaceBusy(ctx context.Context, resourceID string, from, to time.Time, busy []Busy) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("booking: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM booking_calendar_busy WHERE resource_id = ?`, resourceID); err != nil {
		return fmt.Errorf("booking: clear busy: %w", err)
	}
	for _, b := range busy {
		// A zero-length or backwards range is not worth a constraint violation;
		// Google emits neither, but a calendar API is not a thing to be trusting
		// about and one bad range must not discard a good poll.
		if !b.EndsAt.After(b.StartsAt) {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO booking_calendar_busy (resource_id, starts_at, ends_at) VALUES (?, ?, ?)`,
			resourceID, b.StartsAt.Unix(), b.EndsAt.Unix()); err != nil {
			return fmt.Errorf("booking: store busy: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO booking_calendar_sync (resource_id, fetched_at, window_from, window_to, error)
		 VALUES (?, ?, ?, ?, '')
		 ON CONFLICT (resource_id) DO UPDATE SET
		   fetched_at = excluded.fetched_at,
		   window_from = excluded.window_from,
		   window_to = excluded.window_to,
		   error = ''`,
		resourceID, d.now().UTC().Unix(), from.Unix(), to.Unix()); err != nil {
		return fmt.Errorf("booking: record sync: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("booking: commit: %w", err)
	}
	return nil
}

// RecordSyncFailure notes that a poll failed, leaving the cached ranges alone.
//
// The cache is not cleared, which is the entire point of it being a cache: the
// ranges are the best answer available and stay in service until a poll replaces
// them. What changes is that an operator can now see the failure, instead of
// inferring it from a calendar that stopped changing.
func (d *Desk) RecordSyncFailure(ctx context.Context, resourceID, message string) error {
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO booking_calendar_sync (resource_id, error) VALUES (?, ?)
		 ON CONFLICT (resource_id) DO UPDATE SET error = excluded.error`,
		resourceID, message); err != nil {
		return fmt.Errorf("booking: record sync failure: %w", err)
	}
	return nil
}

// SyncState reports what each resource's calendar last did.
func (d *Desk) SyncState(ctx context.Context) ([]Sync, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT r.id, r.google_calendar_id,
		        COALESCE(s.fetched_at, 0), COALESCE(s.error, '')
		 FROM booking_resources r
		 LEFT JOIN booking_calendar_sync s ON s.resource_id = r.id
		 WHERE r.active = 1
		 ORDER BY r.id`)
	if err != nil {
		return nil, fmt.Errorf("booking: sync state: %w", err)
	}
	defer rows.Close()

	out := make([]Sync, 0, 8)
	for rows.Next() {
		var s Sync
		var ts int64
		if err := rows.Scan(&s.ResourceID, &s.CalendarID, &ts, &s.Error); err != nil {
			return nil, fmt.Errorf("booking: sync state: %w", err)
		}
		if ts > 0 {
			s.FetchedAt = time.Unix(ts, 0).UTC()
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Unmirrored lists confirmed bookings that are not in the owner's calendar yet.
//
// This is the retry queue, and it is a query rather than a table because the
// condition already exists in the data: a confirmed booking with no event id has
// not been mirrored. A separate queue would be a second copy of that fact, free
// to disagree with it.
//
// Past bookings are skipped. An event written into last Tuesday helps nobody, and
// retrying it forever is a worker that never drains.
func (d *Desk) Unmirrored(ctx context.Context, limit int) ([]Booking, error) {
	rows, err := d.db.QueryContext(ctx,
		selectBooking+` WHERE status = 'confirmed'
		   AND gcal_event_id IS NULL
		   AND starts_at > ?
		 ORDER BY starts_at LIMIT ?`,
		d.now().UTC().Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("booking: unmirrored: %w", err)
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

// AttachEvent records that a booking now exists in the owner's calendar.
func (d *Desk) AttachEvent(ctx context.Context, bookingID, eventID string) error {
	if _, err := d.db.ExecContext(ctx,
		`UPDATE bookings SET gcal_event_id = ? WHERE id = ?`, eventID, bookingID); err != nil {
		return fmt.Errorf("booking: attach event: %w", err)
	}
	return nil
}

// Cancelled lists bookings that were cancelled but whose calendar event is still
// standing, so the worker can take it back out. The event id is cleared by
// ForgetEvent once it is gone.
func (d *Desk) Cancelled(ctx context.Context, limit int) ([]Booking, error) {
	rows, err := d.db.QueryContext(ctx,
		selectBooking+` WHERE status = 'cancelled'
		   AND gcal_event_id IS NOT NULL
		 ORDER BY cancelled_at LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("booking: cancelled: %w", err)
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

// ForgetEvent drops the link to a calendar event that no longer exists.
func (d *Desk) ForgetEvent(ctx context.Context, bookingID string) error {
	if _, err := d.db.ExecContext(ctx,
		`UPDATE bookings SET gcal_event_id = NULL WHERE id = ?`, bookingID); err != nil {
		return fmt.Errorf("booking: forget event: %w", err)
	}
	return nil
}

// Resource reads one by id, for the worker that needs its calendar.
func (d *Desk) Resource(ctx context.Context, id string) (Resource, error) {
	var r Resource
	err := d.db.QueryRowContext(ctx,
		`SELECT id, name, google_calendar_id FROM booking_resources WHERE id = ?`, id,
	).Scan(&r.ID, &r.Name, &r.GoogleCalendarID)
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, fmt.Errorf("booking: no resource %q", id)
	}
	if err != nil {
		return Resource{}, fmt.Errorf("booking: resource: %w", err)
	}
	return r, nil
}

// Blackout closes a range, for the operator's console.
func (d *Desk) Blackout(ctx context.Context, resourceID string, from, to time.Time, reason string) error {
	if !to.After(from) {
		return fmt.Errorf("booking: blackout ends before it starts")
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO booking_blackouts (id, resource_id, starts_at, ends_at, reason, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		newID(), nullIfEmpty(resourceID), from.Unix(), to.Unix(), reason,
		d.now().UTC().Unix()); err != nil {
		return fmt.Errorf("booking: blackout: %w", err)
	}
	return nil
}
