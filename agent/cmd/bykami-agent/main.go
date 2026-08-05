// Command bykami-agent is the booth binary.
//
// One process on the studio PC: it watches the camera's hot folder, owns the
// print queue, serves the kiosk UI at http://localhost, and deletes the
// originals seven days later. Chrome in kiosk mode is the only other moving
// part.
//
// Not Electron. Electron's value here was a local process with hardware access;
// a Go binary is that, in the language api/ already uses, and it cross-compiles
// from macOS with GOOS=windows — where Electron for Windows could not be built
// or run on this machine at all.
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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/catalog"
	"github.com/bhaktiyudha/bykami/agent/internal/clip"
	"github.com/bhaktiyudha/bykami/agent/internal/compose"
	"github.com/bhaktiyudha/bykami/agent/internal/derive"
	"github.com/bhaktiyudha/bykami/agent/internal/framesync"
	"github.com/bhaktiyudha/bykami/agent/internal/httpd"
	"github.com/bhaktiyudha/bykami/agent/internal/ingest"
	"github.com/bhaktiyudha/bykami/agent/internal/payment"
	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/printer"
	"github.com/bhaktiyudha/bykami/agent/internal/purge"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
	"github.com/bhaktiyudha/bykami/agent/internal/shutter"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

type config struct {
	addr       string
	root       string
	hotFolder  string
	outlet     string
	source     string
	camera     string
	shutter    string
	payments   string
	printerKit string
	printQueue string
	printCut   string
	printWait  time.Duration
	templates  string
	frameSync  string
	syncEvery  time.Duration
	retention  time.Duration
	autoSettle time.Duration
	speed      float64

	publicHost  string
	accessToken string
}

func main() {
	var c config

	// localhost, and this is not a preference. The kiosk runs in a browser on
	// this machine; nothing else has any business reaching it.
	flag.StringVar(&c.addr, "addr", "127.0.0.1:8899", "listen address; localhost because the kiosk browser is the only client")
	flag.StringVar(&c.root, "root", "bykami-booth", "directory for the database, sessions, sheets and derivatives")
	flag.StringVar(&c.hotFolder, "hot-folder", "", "directory the camera's tethering software writes into; empty disables the watcher")
	flag.StringVar(&c.outlet, "outlet", "jajag", "outlet id stamped on every session")

	// Two capture sources, and the difference is not cosmetic. See the
	// resolution table in design/kiosk.md: 1080p is about 180 dpi at 4R.
	flag.StringVar(&c.source, "source", "hotfolder", `where frames come from: "hotfolder" (a tethered camera), "hybrid" (tethered, with a live preview) or "webcam" (development)`)

	// Which camera the kiosk previews, by label substring. Only consulted on
	// the sources that show a preview at all.
	//
	// Needed because getUserMedia with no device named hands over the browser's
	// default, and on a booth PC that is the lid webcam rather than the Canon —
	// the integrated camera wins and the DSLR is never opened.
	flag.StringVar(&c.camera, "camera", "", `preview this video device, matched as a case-insensitive substring of its label, e.g. "EOS"; empty takes the browser's default camera`)

	// How the countdown reaches the camera without the relay hardware in
	// design/kiosk.md. A URL rather than a vendor integration, for the same
	// reason the hot folder is a directory: whatever already owns the camera
	// can be asked to fire it, and the booth learns no driver.
	flag.StringVar(&c.shutter, "shutter", "", `URL that fires the camera when the countdown ends, e.g. http://127.0.0.1:5513/?slc=capturenoaf (digiCamControl); empty means the shutter is pressed by hand`)

	// Empty by default, and that default is a safety property. With no provider
	// the booth cannot take money, so it says "pay at the counter" instead of
	// opening the shutter for free.
	flag.StringVar(&c.payments, "payments", "", `payment provider: "" (disabled) or "sim" (development only — takes no money)`)
	flag.StringVar(&c.printerKit, "printer", "sim", `print backend: "sim" (development) or "dnp" (the DS-RX1HS through its Windows driver)`)

	// Two queues against the one printer, because the cut is the customer's
	// choice per session and the driver holds it as a machine-wide setting.
	// See internal/printer/spooler.go for why that is not worked around in
	// code.
	flag.StringVar(&c.printQueue, "printer-queue", "", `with -printer=dnp, the Windows print queue that returns a whole sheet, e.g. "DS-RX1"`)
	flag.StringVar(&c.printCut, "printer-cut-queue", "", `with -printer=dnp, a second queue on the same printer whose driver has the 2-inch cut enabled`)
	flag.DurationVar(&c.printWait, "printer-wait", printer.DefaultSpoolWait, "with -printer=dnp, how long past the expected print time a job may wait on the machine — for paper, a jam, or the printer coming back online — before it is cancelled")
	flag.StringVar(&c.templates, "templates", "", "extra template directory; the built-in designs are always available")

	// Where the operator console's frame catalogue lives. Empty disables
	// syncing entirely, which is the state of a booth nobody has enrolled — it
	// starts and sells photos with the designs it has.
	flag.StringVar(&c.frameSync, "frame-sync", "", "base URL of the cloud API to pull published frames from, e.g. https://app.bykami.id")
	flag.DurationVar(&c.syncEvery, "frame-sync-every", framesync.DefaultInterval, "how often to poll the cloud for new frames")
	flag.DurationVar(&c.retention, "retention", purge.DefaultAge, "how long originals stay on this PC")
	flag.DurationVar(&c.autoSettle, "sim-auto-settle", 0, "with -payments=sim, settle a charge after this long with nobody pressing anything")
	flag.Float64Var(&c.speed, "sim-print-speed", 1, "with -printer=sim, divide the manufacturer's print time by this")

	// A real booth sets neither. The kiosk is a screen wired to the PC beside
	// it; a public address is a test-deployment concession, taken because
	// getUserMedia will not run on an insecure origin and a phone cannot reach
	// localhost.
	flag.StringVar(&c.publicHost, "public-host", "", "extra hostname to answer to, for a tunnelled test deployment; needs -access-token")
	flag.StringVar(&c.accessToken, "access-token", "", "secret admitting requests to -public-host; comma-separated for one per tester; read from BYKAMI_ACCESS_TOKEN when unset")
	flag.Usage = usage
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Subcommands come after the flags so that -root applies to them, which is
	// the only flag they need.
	if args := flag.Args(); len(args) > 0 {
		if args[0] != "media" {
			log.Error("unknown command", "command", args[0])
			usage()
			os.Exit(2)
		}
		if err := media(c.root, args[1:]); err != nil {
			log.Error("media", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(c, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintln(out, "bykami-agent — the booth binary.")
	fmt.Fprintln(out, "\nRun the booth:")
	fmt.Fprintln(out, "  bykami-agent -hot-folder C:\\bykami\\hot -payments sim")
	fmt.Fprintln(out, "\nCount the printer's media (not an HTTP route, on purpose):")
	fmt.Fprintln(out, "  bykami-agent media status")
	fmt.Fprintln(out, "  bykami-agent media load 700 \"roll 1\"")
	fmt.Fprintln(out, "  bykami-agent media adjust -5 \"jam, five sheets wasted\"")
	fmt.Fprintln(out, "\nFlags:")
	flag.PrintDefaults()
}

func run(c config, log *slog.Logger) error {
	root, err := filepath.Abs(c.root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}

	source, err := parseSource(c.source)
	if err != nil {
		return err
	}
	if source.Tethered() && c.hotFolder == "" {
		return fmt.Errorf("-source=%s needs -hot-folder pointing at the camera's destination directory", source)
	}

	// A hybrid booth with no -camera previews whatever the browser calls
	// default, which on a PC with a built-in webcam is the built-in webcam —
	// the customer poses at the lid camera while the Canon photographs them
	// from somewhere else. Refused rather than warned: the two cameras look
	// identical on the screen, so nobody discovers this until they see prints
	// that do not match what anybody was looking at.
	if source == httpd.SourceHybrid && c.camera == "" {
		return errors.New(`-source=hybrid needs -camera naming the tethered camera's video device, e.g. -camera EOS`)
	}

	// The browser owns the camera on the webcam source, so there is nothing
	// here for a remote trigger to fire. Refused rather than ignored: a booth
	// started with a shutter it will never use is a booth somebody believes is
	// firing a camera.
	var fireShutter func(context.Context) error
	if c.shutter != "" {
		if !source.Tethered() {
			return fmt.Errorf("-shutter has nothing to fire on -source=%s: the browser owns the camera there", source)
		}
		release, err := shutter.New(c.shutter, shutter.DefaultTimeout)
		if err != nil {
			return err
		}
		fireShutter = release.Fire
	}

	db, err := store.Open(filepath.Join(root, "booth.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	photos := photo.New(db)
	clips := clip.New(db)
	sessions := session.New(db)

	provider, simulated, err := newPaymentProvider(c, log)
	if err != nil {
		return err
	}
	payments := payment.New(db, provider)

	backend, err := newPrintBackend(c, log)
	if err != nil {
		return err
	}
	prints := printer.New(db, backend, log)

	watcher := ingest.New(c.hotFolder, root, photos, sessions, log)

	// Synced frames land here, under the booth's own root: it is state the
	// booth maintains, like the database and the sheets, not something an
	// operator edits.
	syncDir := filepath.Join(root, "frames")

	templates, err := loadTemplates(syncDir, c.templates, log)
	if err != nil {
		return err
	}
	live := compose.NewSet(templates)

	packages, err := catalog.All()
	if err != nil {
		return err
	}

	// Preferred over the flag, and the flag is kept only for a quick local run.
	// A token in argv is visible to every process on the box via ps, and a
	// token in a systemd unit is world-readable in /etc/systemd; an
	// EnvironmentFile can be mode 0600.
	token := c.accessToken
	if token == "" {
		token = os.Getenv("BYKAMI_ACCESS_TOKEN")
	}
	tokens := splitTokens(token)

	// The booth's credential for the cloud catalogue. Environment only — there
	// is no flag — for the same reason the access token prefers one: argv is
	// world-readable through ps.
	frameSync := framesync.New(c.frameSync, os.Getenv("BYKAMI_BOOTH_TOKEN"), syncDir, c.syncEvery,
		func() error {
			ts, err := loadTemplates(syncDir, c.templates, log)
			if err != nil {
				return err
			}
			live.Store(ts)
			log.Info("templates reloaded", "templates", len(ts))
			return nil
		}, log)

	srv, err := httpd.New(httpd.Deps{
		Sessions: sessions, Photos: photos, Payments: payments, Printer: prints,
		Clips:  clips,
		Ingest: watcher, Templates: live, Packages: packages,
		Root: root, Source: source, Camera: c.camera, Shutter: fireShutter, OutletID: c.outlet,
		Simulated: simulated, PublicHost: c.publicHost, AccessTokens: tokens,
		Retention: c.retention,
		Log:       log,
	})
	if err != nil {
		return err
	}
	if c.publicHost != "" {
		log.Warn("PUBLIC HOSTNAME ENABLED: this booth is reachable from the internet behind a shared token — test deployments only",
			"host", c.publicHost)
	}

	// SIGTERM on Linux, and Ctrl-C anywhere. A booth PC is more likely to lose
	// power than to be asked politely, which is why nothing here depends on a
	// clean shutdown for correctness — the recovery scan and the interrupted-job
	// reconciliation are what actually make a restart safe.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var wg sync.WaitGroup
	background := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("worker stopped", "worker", name, "err", err)
			}
		}()
	}

	if c.hotFolder != "" {
		background("ingest", watcher.Run)
	} else {
		// Not an error: the webcam source has no hot folder, and neither does a
		// developer poking at the API.
		log.Warn("no hot folder configured; frames can only arrive from the kiosk")
	}

	background("printer", func(ctx context.Context) error {
		return prints.Run(ctx, func(j printer.Job) (string, error) {
			return filepath.Join(root, filepath.FromSlash(j.SheetPath)), nil
		})
	})
	background("purge", purge.New(photos, clips, root, c.retention, log).Run)

	if frameSync != nil {
		background("framesync", frameSync.Run)
	} else {
		log.Info("frame sync is off; this booth offers only the designs already installed")
	}

	// The review screen is the one the customer taps through, and without this
	// it paints full-resolution originals into thumbnails — 24 MP each on the
	// tethered path. Background rather than inline in the capture handler: the
	// shutter path is where latency is the product.
	background("derive", derive.NewWorker(photos, root, log).Run)

	// The moving version of each frame — the seconds of camera before the
	// shutter, rendered to a GIF for the download page. Heavier than derive by
	// an order of magnitude and wanted later, so it takes one clip per pass;
	// see clip.DefaultBatch.
	// WithSheets is what turns on the animation of the whole frame, as opposed
	// to one face at a time. It is handed the live template set rather than the
	// slice loaded at startup, so a sheet queued against a design that arrived
	// from the catalogue mid-session still finds it.
	background("clip", clip.NewWorker(clips, root, log).WithSheets(live, photos).Run)

	httpSrv := &http.Server{
		Addr:    c.addr,
		Handler: srv.Handler(),
		// Generous, because one of these requests carries a full-resolution
		// frame from the browser over the loopback interface.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("booth listening",
			"addr", c.addr, "root", root, "source", source,
			"hot_folder", c.hotFolder, "outlet", c.outlet,
			// Whether the booth fires its own camera decides whether staff have
			// to stand next to it, so it does not belong only in the unit file.
			"shutter", fireShutter != nil,
			"templates", live.Len(), "packages", len(packages),
			"payments", providerName(provider), "printer", backend.Name(),
			// Whether frames are being pulled is otherwise only answerable by
			// reading the unit file, because a sync that finds nothing changed
			// is deliberately silent — so "is sync even on?" had no answer in
			// the log at all.
			"frame_sync", frameSyncTarget(frameSync, c.frameSync))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = httpSrv.Shutdown(shutdownCtx)
	wg.Wait()
	return err
}

func parseSource(s string) (httpd.Source, error) {
	switch httpd.Source(s) {
	case httpd.SourceHotFolder:
		return httpd.SourceHotFolder, nil
	case httpd.SourceWebcam:
		return httpd.SourceWebcam, nil
	case httpd.SourceHybrid:
		return httpd.SourceHybrid, nil
	default:
		return "", fmt.Errorf(`unknown -source %q: want "hotfolder", "hybrid" or "webcam"`, s)
	}
}

// newPaymentProvider picks the gateway and returns the simulated one separately
// when that is what was chosen.
//
// The two travel together on purpose: the "the customer paid" button exists
// only when the simulated provider does, so a booth that could take real money
// has no route to a free session.
func newPaymentProvider(c config, log *slog.Logger) (payment.Provider, *payment.Simulated, error) {
	switch c.payments {
	case "":
		// The deployed default. QRIS means Xendit, which is blocked on a
		// business entity, NPWP and a bank account — days to weeks entirely
		// outside the build. Until then the booth refuses to start a session and
		// says so, rather than opening the shutter for nothing.
		log.Warn("no payment provider configured; the booth will refuse to start sessions")
		return nil, nil, nil
	case "sim":
		log.Warn("SIMULATED PAYMENTS: no money is taken and any session can be unlocked from the screen — development only")
		sim := payment.NewSimulated(log, c.autoSettle)
		return sim, sim, nil
	default:
		return nil, nil, fmt.Errorf(`unknown -payments %q: want "" or "sim"`, c.payments)
	}
}

func newPrintBackend(c config, log *slog.Logger) (printer.Backend, error) {
	switch c.printerKit {
	case "sim":
		log.Warn("SIMULATED PRINTER: nothing comes out of the machine — development only")
		return printer.NewSimulated(log, c.speed), nil
	case "dnp":
		return printer.NewSpooler(printer.SpoolerConfig{
			Queue:    c.printQueue,
			CutQueue: c.printCut,
			Wait:     c.printWait,
		}, log)
	default:
		return nil, fmt.Errorf(`unknown -printer %q: want "sim" or "dnp"`, c.printerKit)
	}
}

// loadTemplates returns the designs this booth can offer, in order of
// increasing authority: the built-ins, then whatever the cloud catalogue has
// been synced into syncDir, then a local directory an operator pointed at.
//
// Local last on purpose. The cloud is the franchise's shared catalogue and
// should override a stale built-in, but somebody standing at the booth with a
// file is making a deliberate, specific decision about that machine, and their
// copy should not be silently replaced by the next poll.
//
// A broken design is reported and skipped rather than fatal: a booth that will
// not start because somebody dropped in a bad template.json is worse than one
// that starts with the designs that work.
func loadTemplates(syncDir, localDir string, log *slog.Logger) ([]compose.Template, error) {
	builtin, err := compose.Builtin()
	if err != nil {
		return nil, fmt.Errorf("built-in templates: %w", err)
	}

	out := make([]compose.Template, 0, len(builtin))
	out = append(out, builtin...)

	for _, dir := range []string{syncDir, localDir} {
		if dir == "" {
			continue
		}
		extra, err := compose.LoadDir(dir)
		if err != nil {
			log.Error("template directory has a problem; the designs that loaded are still available",
				"dir", dir, "err", err)
		}
		out = overlay(out, extra)
	}
	return out, nil
}

// overlay adds ts to out, replacing any design already there with the same id.
func overlay(out, ts []compose.Template) []compose.Template {
	for _, t := range ts {
		replaced := false
		for i := range out {
			if out[i].ID == t.ID {
				out[i] = t
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, t)
		}
	}
	return out
}

// frameSyncTarget describes the frame sync for the startup log: where it pulls
// from, or why it is not pulling.
func frameSyncTarget(w *framesync.Worker, base string) string {
	switch {
	case w != nil:
		return base
	case base != "":
		// Configured but inert, which is the confusing case: the flag is right
		// there in the unit and nothing happens, because the token is missing.
		return "disabled (BYKAMI_BOOTH_TOKEN is not set)"
	default:
		return "disabled"
	}
}

// splitTokens parses the comma-separated access token setting.
//
// Blanks are dropped rather than becoming a token that an empty ?t= matches,
// which is what a trailing comma would otherwise leave behind — and the failure
// mode of that mistake is an open booth.
func splitTokens(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func providerName(p payment.Provider) string {
	if p == nil {
		return "disabled"
	}
	return p.Name()
}
