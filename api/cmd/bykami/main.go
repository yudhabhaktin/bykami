// Command bykami is the modular monolith behind the bykami platform.
//
// One binary, not three services. Identity, loyalty, and booking are modules
// with clean internal boundaries — on a single core, splitting them would mean
// three Go runtimes, three GC heaps, and localhost round trips for no gain,
// since there is no independent scaling to be had. Splitting later is a
// refactor rather than a rewrite, and worth doing only when one module actually
// needs to scale apart from the others.
//
// It listens on localhost. Cloudflare Tunnel dials out to reach it, so the VPS
// needs no public IP and nothing here depends on a firewall rule staying right.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/admin"
	"github.com/bhaktiyudha/bykami/api/internal/booking"
	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/gcal"
	"github.com/bhaktiyudha/bykami/api/internal/httpapi"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/instagram"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/mfa"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address; localhost because Cloudflare Tunnel dials out to it")
	dsn := flag.String("db", "bykami.db", "path to the SQLite database")
	// Empty by default, and that default is a safety property rather than a
	// placeholder. With no delivery configured the auth routes answer 503, so a
	// box nobody explicitly switched on cannot start taking real customer
	// logins — which is exactly the state infrastructure.md requires until data
	// residency is settled. "log" is the development sender and says so loudly.
	otpDelivery := flag.String("otp-delivery", "", `how one-time codes are delivered: "" (disabled) or "log" (development only)`)
	// Who may use the operator console, as a comma-separated list of phone
	// numbers. Configuration rather than a database column on purpose — see
	// internal/admin: a role in a table has a bootstrap problem whose usual
	// answer is a seed script that becomes a way to grant admin. Empty means
	// nobody, which is the safe default and the deployed one.
	adminPhones := flag.String("admin-phones", "", "comma-separated operator phone numbers allowed into the admin console")
	// How far ahead the booking calendar is open. 31 days is what the studio's
	// previous booking pages offered, so it is the number its customers are used
	// to; a flag rather than a constant because it is the kind of thing an owner
	// asks to change before a holiday.
	bookingWindowDays := flag.Int("booking-window-days", 31, "how many days ahead bookings may be made")
	// Extra CORS origins for the booking routes, on top of the four *.bykami.id
	// sites, which are always allowed. This is how `astro dev` on
	// http://localhost:4321 reaches a local API. Empty in production, and it has
	// to stay that way: these routes write.
	bookingOrigins := flag.String("booking-origins", "", "extra CORS origins for booking, comma-separated (development only)")
	flag.Usage = usage
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Subcommands come after the flags so that -db applies to them, which is the
	// only flag they need.
	if args := flag.Args(); len(args) > 0 {
		var err error
		switch args[0] {
		case "frames":
			err = frameCmd(*dsn, args[1:])
		case "booking":
			err = bookingCmd(*dsn, args[1:])
		case "admin":
			err = adminCmd(*dsn, *adminPhones, args[1:])
		default:
			log.Error("unknown command", "command", args[0])
			usage()
			os.Exit(2)
		}
		if err != nil {
			log.Error(args[0], "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(*addr, *dsn, *otpDelivery, *adminPhones, *bookingOrigins, *bookingWindowDays, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintln(out, "bykami — the platform monolith and operator console.")
	fmt.Fprintln(out, "\nRun the server:")
	fmt.Fprintln(out, "  bykami -addr 127.0.0.1:8080 -db /var/lib/bykami/bykami.db")
	fmt.Fprintln(out, "\nLet an operator into the console. Prints a QR code to scan with any")
	fmt.Fprintln(out, "authenticator app; the number must also be in -admin-phones to be worth")
	fmt.Fprintln(out, "anything. This is a shell job because the console cannot enrol its own")
	fmt.Fprintln(out, "first operator — that would need the login it does not have yet:")
	fmt.Fprintln(out, "  bykami -db … admin enroll 081234567890")
	fmt.Fprintln(out, "  bykami -db … admin enroll 081234567890 /tmp/qr.png   # if the terminal cannot draw it")
	fmt.Fprintln(out, "  bykami -db … admin list")
	fmt.Fprintln(out, "  bykami -db … admin unlock 081234567890")
	fmt.Fprintln(out, "  bykami -db … admin revoke 081234567890               # a lost phone")
	fmt.Fprintln(out, "\nManage the frame catalogue (there is a console page for this too):")
	fmt.Fprintln(out, "  bykami -db … frames list")
	fmt.Fprintln(out, "  bykami -db … frames import strip-4.png \"Klasik Empat\" klasik")
	fmt.Fprintln(out, "  bykami -db … frames publish klasik-empat")
	fmt.Fprintln(out, "  bykami -db … frames season ramadan-2027 2027-02-08 2027-03-09")
	fmt.Fprintln(out, "\nEnvironment (secrets belong here, not in argv, where ps would show them):")
	fmt.Fprintln(out, "  BYKAMI_BOOTH_TOKEN         shared secret booths present; unset leaves /v1/booth/* at 503")
	fmt.Fprintln(out, "  BYKAMI_INSTAGRAM_TOKEN     long-lived Instagram token; unset disables the mirror")
	fmt.Fprintln(out, "  BYKAMI_INSTAGRAM_ACCOUNT   the handle being mirrored, for labelling only")
	fmt.Fprintln(out, "  BYKAMI_INSTAGRAM_BASE      override Meta's API host and version")
	fmt.Fprintln(out, "  BYKAMI_GOOGLE_CREDENTIALS  base64 of a Google service-account key file; unset disables")
	fmt.Fprintln(out, "  BYKAMI_GOOGLE_OAUTH_CLIENT_ID      OAuth client for connecting calendars from the console")
	fmt.Fprintln(out, "  BYKAMI_GOOGLE_OAUTH_CLIENT_SECRET  its secret; both unset means share calendars by hand")
	fmt.Fprintln(out, "                             calendar sync and booking then runs from the database alone")
	fmt.Fprintln(out, "\nThe Instagram token rotates. What is in the environment is only a seed:")
	fmt.Fprintln(out, "it is written to the database on first start and refreshed there from then")
	fmt.Fprintln(out, "on, so changing it here does nothing until the stored row is cleared:")
	fmt.Fprintln(out, "  sqlite3 bykami.db 'DELETE FROM instagram_token'")
	fmt.Fprintln(out, "\nThe Google credential is a service-account key file, base64-encoded because it")
	fmt.Fprintln(out, "is multi-line PEM and a systemd EnvironmentFile has no syntax for that:")
	fmt.Fprintln(out, "  base64 -w0 < service-account.json")
	fmt.Fprintln(out, "Each calendar must then be shared with the service account's own address, at")
	fmt.Fprintln(out, "\"Make changes to events\". The address is logged at startup and shown on the")
	fmt.Fprintln(out, "console's Pengaturan page, which is also where a calendar id is entered.")
	fmt.Fprintln(out, "\nSeed the booking catalogue (hours, breaks and packages have no console page):")
	fmt.Fprintln(out, "  bykami -db … booking seed")
	fmt.Fprintln(out, "  bykami -db … booking resources")
	fmt.Fprintln(out, "  bykami -db … booking calendar photobox-y2k studio@group.calendar.google.com")
	fmt.Fprintln(out, "\nFlags:")
	flag.PrintDefaults()
}

func run(addr, dsn, otpDelivery, adminPhones, bookingOrigins string, bookingWindowDays int, log *slog.Logger) error {
	sender, authEnabled, err := newSender(otpDelivery, log)
	if err != nil {
		return err
	}

	db, err := store.Open(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	// Wired here and nowhere else. Modules take their dependencies as
	// parameters, which is what keeps the boundaries real rather than aspirational.
	ident := identity.New(db, sender)
	ledger := loyalty.New(db)
	catalogue := frames.New(db)
	// What the booths say they are offering, which is the catalogue plus the
	// designs compiled into the agent binary plus whatever is in each machine's
	// own templates folder. Written by the booths, read by the console.
	booths := frames.NewBooths(db)

	// The booth sync secret, from the environment rather than a flag. A token
	// in argv is readable by every process on the box through ps; an
	// EnvironmentFile can be mode 0600. The agent reads its half of this pair
	// the same way, for the same reason.
	//
	// Empty leaves /v1/booth/* answering 503, which is the default and means a
	// deployment that was never given a secret serves no catalogue rather than
	// serving it to anyone.
	boothToken := os.Getenv("BYKAMI_BOOTH_TOKEN")
	log.Info("booth sync configured", "enabled", boothToken != "")

	// The Instagram mirror, from the environment for the same reason as the
	// booth token, plus one of its own: this one rotates. What is read here is
	// only ever a seed — after the first refresh the database holds a token
	// this file has never seen, so changing the value here does nothing until
	// the stored row is cleared as well.
	mirror := instagram.New(db)
	igToken := os.Getenv("BYKAMI_INSTAGRAM_TOKEN")
	igAccount := os.Getenv("BYKAMI_INSTAGRAM_ACCOUNT")

	desk := booking.New(db, time.Duration(bookingWindowDays)*24*time.Hour)

	// The owner's Google Calendar, from the environment because it is a signing
	// key. Nil when unset, which is the normal state of a box nobody has connected
	// — booking then runs entirely from this database, which is why the busy
	// ranges it reads are a cache and not a source.
	calendar, err := gcal.New(os.Getenv("BYKAMI_GOOGLE_CREDENTIALS"))
	if err != nil {
		// Fatal, unlike the mirror. A credential that was supplied and cannot be
		// parsed is a typo somebody needs to hear about now, not a calendar that
		// silently never syncs.
		return fmt.Errorf("google calendar: %w", err)
	}
	if calendar != nil {
		log.Info("google calendar configured", "service_account", calendar.Email())
	} else {
		log.Info("google calendar configured", "enabled", false)
	}

	// One worker, shared. The background loop runs it on a ticker; the operator
	// console runs it on demand from the settings page, so somebody connecting a
	// calendar finds out whether they shared it correctly without waiting for the
	// next tick or reading the journal.
	//
	// Nil when there is no credential, which both callers check — calendarFor
	// returns a nil interface rather than a typed nil for exactly that reason.
	calWorker := booking.NewWorker(desk, calendarFor(calendar), log, 0, studioLocation)

	api := httpapi.New(httpapi.Config{
		Identity: ident, Loyalty: ledger, Frames: catalogue, Booths: booths, Booking: desk,
		Instagram: mirror, InstagramAccount: igAccount,
		Health: db.PingContext, Log: log,
		AuthEnabled: authEnabled, BoothToken: boothToken,
		BookingOrigins: splitOrigins(bookingOrigins),
	})

	// The console's authenticators. Unlike every other credential here this one
	// needs nothing from the environment: the secrets are in the database, put
	// there by `bykami admin enroll`, and there is no provider to sign up with.
	// That is the whole point of it — the console had no usable login at all
	// while it waited on a WhatsApp account.
	auth := mfa.New(db)
	enrolled, err := auth.Count(context.Background())
	if err != nil {
		return fmt.Errorf("admin authenticators: %w", err)
	}

	// The console's own way to share a calendar with the service account above.
	// Nil when unconfigured, which leaves the paste-a-calendar-id form as the
	// only route — the share is then made by hand inside Google Calendar, as it
	// was before. Not fatal when absent, unlike the signing key: nothing stops
	// working without it, an operator just has more typing to do.
	connect := gcal.NewConnect(
		os.Getenv("BYKAMI_GOOGLE_OAUTH_CLIENT_ID"),
		os.Getenv("BYKAMI_GOOGLE_OAUTH_CLIENT_SECRET"))
	if connect != nil {
		log.Info("google connect configured")
	}

	console, err := admin.New(ident, ledger, catalogue, booths, desk, calWorker, auth, connect, log, splitPhones(adminPhones))
	if err != nil {
		return fmt.Errorf("admin console: %w", err)
	}
	// Both numbers, because either being zero means nobody can sign in, and the
	// two are fixed in different places: the allow-list in the service
	// configuration, the enrolments with a subcommand.
	log.Info("admin console configured",
		"operators", len(splitPhones(adminPhones)), "authenticators", enrolled)

	// The URL space is split here rather than inside either package, because
	// this is the only place that knows both exist. Go's ServeMux prefers the
	// more specific pattern, so the console's "/" catch-all does not shadow the
	// API — and an unknown path lands on the console's 404 rather than a bare
	// one, which is what a browser is most likely to hit.
	//
	// This combined mux is what a request with no recognised hostname gets:
	// localhost, the deploy's health probe, `astro dev`. Public traffic arrives
	// under one of the two names and is split between them — see host.go.
	consoleHandler := console.Handler()
	root := http.NewServeMux()
	root.Handle("/", consoleHandler)
	root.Handle("/healthz", api)
	root.Handle("/v1/", api)

	srv := &http.Server{
		Addr:    addr,
		Handler: byHost(consoleHandler, api, root),
		// Bounded so a stalled client cannot pin a connection indefinitely. On a
		// 1 GB box each held connection is memory that the next request needs.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// systemd sends SIGTERM on restart. Draining rather than dropping means a
	// deploy does not fail whatever request happened to be in flight.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Nil when no token is configured, which is the normal state of a
	// deployment nobody has connected to Instagram. The mirror still serves
	// whatever is already stored, so a token that finally expires costs the
	// next update rather than the posts already saved.
	igWorker, err := instagram.NewWorker(ctx, mirror, igToken, os.Getenv("BYKAMI_INSTAGRAM_BASE"), 0, 0, log)
	if err != nil {
		return fmt.Errorf("instagram mirror: %w", err)
	}
	log.Info("instagram mirror configured", "enabled", igWorker != nil, "account", igAccount)
	if igWorker != nil {
		// Not in the errCh select below: a mirror that gives up must not take
		// the platform down with it. Run only returns when ctx is cancelled.
		go func() {
			if err := igWorker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("instagram mirror stopped", "err", err)
			}
		}()
	}

	// The calendar sync loop, on the same terms as the Instagram mirror and for a
	// stronger reason: a studio that cannot reach Google must still be able to sell
	// a slot, so this loop is the thing that falls over, never the booking.
	if calWorker != nil {
		go calWorker.Run(ctx)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "db", dsn, "auth_enabled", authEnabled)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// splitPhones turns the flag's comma-separated list into entries. Blank entries
// are dropped so that a trailing comma is not an error, but nothing else is
// tidied — admin.New normalises and rejects, because a number that cannot be
// parsed should stop startup rather than silently match nobody.
func splitPhones(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// newSender picks the delivery channel and reports whether auth routes may
// open at all. The two travel together on purpose: an auth surface with no way
// to deliver a code is not a working feature, it is a 500 waiting for its first
// caller.
func newSender(kind string, log *slog.Logger) (identity.Sender, bool, error) {
	switch kind {
	case "":
		// Non-nil rather than nil even though the transport gates every route
		// that could reach it. A nil interface here turns a future refactor
		// that drops the gate into a panic instead of an error.
		return disabledSender{}, false, nil
	case "log":
		log.Warn("OTP codes will be written to the log: development only, never production")
		return logSender{log}, true, nil
	default:
		return nil, false, fmt.Errorf(`unknown -otp-delivery %q: want "" or "log"`, kind)
	}
}

type disabledSender struct{}

func (disabledSender) Send(context.Context, string, string) error {
	return errors.New("otp delivery is not configured")
}

// logSender is the placeholder delivery channel. WhatsApp is the intended primary
// and needs a provider account that does not exist yet; until it does, codes go to
// the log so the flow is exercisable end to end.
//
// A one-time code in a log is a one-time code in whatever reads that log, so this
// is off unless somebody passes -otp-delivery=log. What that gate is *not* is a
// guarantee it cannot reach production: the systemd unit passes the flag whenever
// app_otp_delivery is set, which is how the operator console is opened at all
// before a WhatsApp account exists. The trade and how to close it again are
// written down in ansible/README.md — because the honest version of this comment
// is a documented decision, not a claim that the situation cannot arise.
//
// Every send is logged at Warn with the code in it, which is deliberate: it makes
// the exposure visible in the journal rather than silent.
type logSender struct{ log *slog.Logger }

func (s logSender) Send(_ context.Context, e164, code string) error {
	s.log.Warn("OTP delivery is not configured; code written to log", "phone", e164, "code", code)
	return nil
}
