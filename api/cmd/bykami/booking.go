package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

// bookingCmd sets up the booking catalogue from the shell.
//
// Same reasoning as frameCmd, and the same expiry: this was written when the
// console had no usable login, and `bykami admin enroll` has since given it one.
// It stays for the reason that outlived the original — there is no console page
// for any of this, and resources and packages change when the studio buys a
// room, which is rarer than uploading artwork.
//
//	bykami booking seed
//	bykami booking resources
//	bykami booking services
//	bykami booking calendar photobox studio@group.calendar.google.com
func bookingCmd(dsn string, args []string) error {
	if len(args) == 0 {
		return errors.New(`booking: want "seed", "resources", "services" or "calendar"`)
	}

	db, err := store.Open(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	desk := booking.New(db, 0)
	ctx := context.Background()

	switch args[0] {
	case "seed":
		return bookingSeed(ctx, db)

	case "resources":
		return bookingResources(ctx, desk)

	case "services":
		return bookingServices(ctx, desk)

	case "calendar":
		if len(args) != 3 {
			return errors.New(`booking calendar: want <resource> <google-calendar-id>`)
		}
		return bookingCalendar(ctx, db, args[1], args[2])

	default:
		return fmt.Errorf("booking: unknown command %q", args[0])
	}
}

// seedService is one row of the catalogue, as it was actually sold.
type seedService struct {
	id, resource, name, line, description string
	price                                 int64
	perPerson                             bool
	duration, buffer                      int
	min, max                              int
}

// bookingSeed writes the studio as it trades today.
//
// Every price, duration and headcount band here was read off the studio's own
// YouCanBook.me pages on 2026-08-10, and the off-site packages off
// refs/PRICE LIST DILUAR STUDIO.pdf. That provenance matters: these are not
// invented defaults, they are what customers were being charged the day this
// replaced them, and the same numbers are recorded with their sources in
// packages/content/src/verticals/studio.ts.
//
// Idempotent. Re-running it after the studio changes a price in the database would
// otherwise quietly put the old one back.
func bookingSeed(ctx context.Context, db *sql.DB) error {
	// Three resources, which is the decision the old booking tool got wrong: it
	// presented six choices while serving one shared availability pool, so a
	// photobox booking blocked a self-photo session that could have run at the
	// same time. Whether Pas Photo can truly run alongside self photo is an open
	// question — both need the operator — and it is a row to move if not.
	resources := []struct{ id, name string }{
		{"photobox", "Photobox"},
		{"self-photo", "Self photo"},
		{"photographer", "Fotografer luar studio"},
	}

	services := []seedService{
		// Photobox: 10-minute session, priced per person, two strips each.
		{id: "photobox-y2k", resource: "photobox", name: "Y2K", line: "photobox",
			description: "2 strip print 2R tiap orang", price: 30_000, perPerson: true,
			duration: 10, buffer: 20, min: 1, max: 5},
		{id: "photobox-vintage", resource: "photobox", name: "Vintage", line: "photobox",
			description: "2 strip print 2R tiap orang", price: 25_000, perPerson: true,
			duration: 10, buffer: 20, min: 1, max: 5},
		{id: "photobox-maroon", resource: "photobox", name: "Maroon", line: "photobox",
			description: "2 strip print 2R tiap orang", price: 25_000, perPerson: true,
			duration: 10, buffer: 20, min: 1, max: 5},

		// Self photo, plain backdrop.
		//
		// MINI's five minutes is the one number here that disagrees with the page
		// this replaces, which sold fifteen. Five is used because it is the only
		// session length in the whole catalogue the owner has actually confirmed —
		// packages/content records it as superseding the PDF's fifteen — and
		// because the booth counts it down on the capture screen, so fifteen here
		// would promise a customer three times the session the room gives them.
		// The booking page and the PDF agree on fifteen precisely because the page
		// was set up from the PDF; that makes them one stale source, not two.
		//
		// Availability is unaffected either way: 5 and 15 both round to one
		// half-hour slot. What changes is only what the customer is told.
		{id: "self-mini", resource: "self-photo", name: "MINI", line: "self-photo",
			description: "1 print 4R", price: 45_000, duration: 5, buffer: 25, min: 1, max: 2},
		{id: "self-midi", resource: "self-photo", name: "MIDI", line: "self-photo",
			description: "2 print 4R", price: 70_000, duration: 20, buffer: 10, min: 3, max: 4},
		{id: "self-maxi", resource: "self-photo", name: "MAXI", line: "self-photo",
			description: "2 print 4R", price: 95_000, duration: 20, buffer: 10, min: 5, max: 6},
		{id: "self-big-maxi", resource: "self-photo", name: "BIG MAXI", line: "self-photo",
			description: "3 print 4R", price: 165_000, duration: 25, buffer: 5, min: 7, max: 10},

		// Self photo, patterned backdrop. Absent from packages/content until now —
		// the price list PDFs predate it and only the booking page carried it.
		{id: "motif-midi", resource: "self-photo", name: "MOTIF MIDI", line: "self-photo",
			description: "Background motif, 2 print 4R", price: 80_000,
			duration: 20, buffer: 10, min: 1, max: 4},
		{id: "motif-family", resource: "self-photo", name: "MOTIF FAMILY", line: "self-photo",
			description: "Background motif, 2 print 4R", price: 120_000,
			duration: 20, buffer: 10, min: 5, max: 8},
		{id: "motif-squad", resource: "self-photo", name: "MOTIF SQUAD", line: "self-photo",
			description: "Background motif, 3 print 4R", price: 180_000,
			duration: 25, buffer: 5, min: 9, max: 12},

		// Pas foto. Formal is per person; the other two are priced for a couple.
		{id: "pas-formal", resource: "self-photo", name: "Pas Foto Formal", line: "pas-foto",
			description: "1 background, 1 print 4R, 1 file", price: 50_000, perPerson: true,
			duration: 15, buffer: 0, min: 1, max: 2},
		{id: "pas-marry-me", resource: "self-photo", name: "Marry Me", line: "pas-foto",
			description: "1 background, 2 print 4R, 6 file", price: 90_000,
			duration: 20, buffer: 10, min: 2, max: 2},
		{id: "pas-kedinasan", resource: "self-photo", name: "Pas Foto Kedinasan", line: "pas-foto",
			description: "2 background, 2 baju, 5 print 4R, 10 file", price: 250_000,
			duration: 40, buffer: 20, min: 2, max: 2},

		// Off-site work, from the PDF. Hours rather than minutes, which is why the
		// slot reservation is per half hour rather than per booking — see the
		// migration. Travel time is not modelled: it is an open question for the
		// owner, and a buffer guessed here would be a wrong answer in the schema.
		{id: "photographer-1h", resource: "photographer", name: "Fotografer 1 jam", line: "outdoor-photographer",
			description: "Max 1 jam, semua file edit, 2 print 4R", price: 350_000,
			duration: 60, buffer: 0, min: 1, max: 30},
		{id: "photographer-90m", resource: "photographer", name: "Fotografer 1,5 jam", line: "outdoor-photographer",
			description: "Max 1,5 jam, semua file edit, 4 print 4R", price: 500_000,
			duration: 90, buffer: 0, min: 1, max: 30},
		{id: "photographer-3h", resource: "photographer", name: "Fotografer 3 jam", line: "outdoor-photographer",
			description: "Max 3 jam, semua file edit, 1 print 10R plus frame", price: 850_000,
			duration: 180, buffer: 0, min: 1, max: 30},
		{id: "videographer-3h", resource: "photographer", name: "Videografer 3 jam", line: "videographer",
			description: "Max 3 jam, hasil edit 2–4 menit", price: 650_000,
			duration: 180, buffer: 0, min: 1, max: 30},
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Unix()

	for _, r := range resources {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO booking_resources (id, name, created_at) VALUES (?, ?, ?)
			 ON CONFLICT (id) DO NOTHING`, r.id, r.name, now); err != nil {
			return fmt.Errorf("seed resource %s: %w", r.id, err)
		}
	}

	for i, s := range services {
		perPerson := 0
		if s.perPerson {
			perPerson = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO booking_services
			   (id, resource_id, name, service_line, description, price_idr, price_per_person,
			    duration_minutes, buffer_minutes, headcount_min, headcount_max, order_index, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (id) DO NOTHING`,
			s.id, s.resource, s.name, s.line, s.description, s.price, perPerson,
			s.duration, s.buffer, s.min, s.max, i, now); err != nil {
			return fmt.Errorf("seed service %s: %w", s.id, err)
		}
	}

	// 09:00–21:00 every day, which the owner confirmed in August 2026 and both
	// booking pages served.
	for wd := range 7 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO booking_hours (weekday, opens_at, closes_at) VALUES (?, 540, 1260)
			 ON CONFLICT (weekday) DO NOTHING`, wd); err != nil {
			return fmt.Errorf("seed hours: %w", err)
		}
	}

	// The two prayer breaks, derived rather than asked for: across 31 days of both
	// calendars, 17:30 was blocked every single day and 12:00 was blocked every day
	// except Friday, when the midday break sits at 11:30 for Jumatan.
	breaks := []struct {
		id      string
		weekday any
		from    int
		to      int
		reason  string
	}{
		{"maghrib", nil, 1050, 1080, "Maghrib"},
		{"jumatan", 5, 690, 720, "Jumatan"},
	}
	for wd := range 7 {
		if wd == 5 {
			continue
		}
		breaks = append(breaks, struct {
			id      string
			weekday any
			from    int
			to      int
			reason  string
		}{fmt.Sprintf("dzuhur-%d", wd), wd, 720, 750, "Dzuhur"})
	}
	for _, b := range breaks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO booking_breaks (id, weekday, starts_at, ends_at, reason)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (id) DO NOTHING`,
			b.id, b.weekday, b.from, b.to, b.reason); err != nil {
			return fmt.Errorf("seed break %s: %w", b.id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	fmt.Printf("seeded %d resources, %d services, hours and breaks\n", len(resources), len(services))
	fmt.Println(`connect a calendar with: bykami -db … booking calendar <resource> <google-calendar-id>`)
	return nil
}

func bookingResources(ctx context.Context, desk *booking.Desk) error {
	resources, err := desk.Resources(ctx)
	if err != nil {
		return err
	}
	state, err := desk.SyncState(ctx)
	if err != nil {
		return err
	}
	synced := make(map[string]booking.Sync, len(state))
	for _, s := range state {
		synced[s.ResourceID] = s
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "RESOURCE\tNAME\tCALENDAR\tLAST SYNC\tERROR")
	for _, r := range resources {
		calendar := r.GoogleCalendarID
		if calendar == "" {
			calendar = "—"
		}
		last := "never"
		if s, ok := synced[r.ID]; ok && !s.FetchedAt.IsZero() {
			last = s.FetchedAt.In(booking.WIB).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, calendar, last, synced[r.ID].Error)
	}
	return w.Flush()
}

func bookingServices(ctx context.Context, desk *booking.Desk) error {
	services, err := desk.Services(ctx)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "SERVICE\tRESOURCE\tNAME\tPRICE\tSESSION\tORANG")
	for _, s := range services {
		price := fmt.Sprintf("%d", s.PriceIDR)
		if s.PricePerPerson {
			price += "/org"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%dm\t%d–%d\n",
			s.ID, s.ResourceID, s.Name, price, s.DurationMinutes, s.HeadcountMin, s.HeadcountMax)
	}
	return w.Flush()
}

// bookingCalendar attaches a Google Calendar to a resource. Pass "" to detach,
// which stops the sync without deleting anything already cached.
func bookingCalendar(ctx context.Context, db *sql.DB, resourceID, calendarID string) error {
	res, err := db.ExecContext(ctx,
		`UPDATE booking_resources SET google_calendar_id = ? WHERE id = ?`, calendarID, resourceID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("booking calendar: no resource %q — try `booking resources`", resourceID)
	}

	fmt.Printf("%s → %s\n", resourceID, calendarID)
	fmt.Println("the calendar must be shared with the service account at \"Make changes to events\"")
	return nil
}
