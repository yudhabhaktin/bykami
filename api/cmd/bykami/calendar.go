package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/gcal"
)

// studioLocation goes on every calendar event, so the entry the owner sees is one
// they can navigate from.
//
// A constant here rather than a lookup into packages/content: that catalogue is
// TypeScript the Go side cannot read, and the alternative — a flag — would be a
// third place the studio's address is written down. It matches
// verticals/studio.ts, which is the fact of record.
const studioLocation = "Jalan Yos Sudarso, Jajag Barat (Hotel Surya), Jajag, Banyuwangi"

// calendarFor adapts a *gcal.Client to what booking asks for.
//
// The two packages describe the same three operations in their own types, on
// purpose: booking.Calendar is written in booking's vocabulary so that package
// never imports an HTTP client, and gcal answers in Google's so it can be tested
// against Google's wire format. Neither depends on the other, and this is the one
// place that knows both — the same reasoning that puts the URL-space split in
// main.go rather than inside either handler.
//
// Returns a nil interface for a nil client, which is the part worth being careful
// about: a typed nil pointer wrapped in an interface is not nil, so returning the
// adapter unconditionally would defeat the check in booking.NewWorker and hand it
// something that panics on first use instead of a worker it declines to build.
func calendarFor(c *gcal.Client) booking.Calendar {
	if c == nil {
		return nil
	}
	return calendarAdapter{client: c}
}

type calendarAdapter struct{ client *gcal.Client }

// ServiceAccount satisfies booking.Principal, so the operator console can print
// the address each calendar has to be shared with. Nothing in the sync loop reads
// it — see the interface's own doc.
func (a calendarAdapter) ServiceAccount() string { return a.client.Email() }

func (a calendarAdapter) FreeBusy(ctx context.Context, calendarID string, from, to time.Time) ([]booking.Busy, error) {
	ranges, err := a.client.FreeBusy(ctx, calendarID, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]booking.Busy, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, booking.Busy{StartsAt: r.StartsAt, EndsAt: r.EndsAt})
	}
	return out, nil
}

func (a calendarAdapter) Insert(ctx context.Context, calendarID string, ev booking.Event) (string, error) {
	return a.client.Insert(ctx, calendarID, gcal.Event{
		Summary:     ev.Summary,
		Description: ev.Description,
		Location:    ev.Location,
		StartsAt:    ev.StartsAt,
		EndsAt:      ev.EndsAt,
	})
}

func (a calendarAdapter) Delete(ctx context.Context, calendarID, eventID string) error {
	err := a.client.Delete(ctx, calendarID, eventID)
	// Translated rather than passed through. booking treats its own ErrEventGone as
	// success, and an unmapped error here would leave every event the owner deleted
	// by hand in the retry queue for good.
	if errors.Is(err, gcal.ErrGone) {
		return booking.ErrEventGone
	}
	return err
}

// splitOrigins parses the development CORS list. Same shape as splitPhones, kept
// separate because they validate differently: an origin has to carry its scheme,
// and a bare hostname here would silently never match.
func splitOrigins(s string) []string {
	var out []string
	for _, o := range strings.Split(s, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, strings.TrimSuffix(o, "/"))
		}
	}
	return out
}
