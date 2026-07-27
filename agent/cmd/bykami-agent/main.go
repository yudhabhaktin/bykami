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
	"sync"
	"syscall"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/catalog"
	"github.com/bhaktiyudha/bykami/agent/internal/compose"
	"github.com/bhaktiyudha/bykami/agent/internal/httpd"
	"github.com/bhaktiyudha/bykami/agent/internal/ingest"
	"github.com/bhaktiyudha/bykami/agent/internal/payment"
	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/printer"
	"github.com/bhaktiyudha/bykami/agent/internal/purge"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

type config struct {
	addr       string
	root       string
	hotFolder  string
	outlet     string
	source     string
	payments   string
	printerKit string
	templates  string
	retention  time.Duration
	autoSettle time.Duration
	speed      float64
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
	flag.StringVar(&c.source, "source", "hotfolder", `where frames come from: "hotfolder" (a tethered camera) or "webcam" (development)`)

	// Empty by default, and that default is a safety property. With no provider
	// the booth cannot take money, so it says "pay at the counter" instead of
	// opening the shutter for free.
	flag.StringVar(&c.payments, "payments", "", `payment provider: "" (disabled) or "sim" (development only — takes no money)`)
	flag.StringVar(&c.printerKit, "printer", "sim", `print backend: "sim" (development). The DNP driver is not built yet`)
	flag.StringVar(&c.templates, "templates", "", "extra template directory; the built-in designs are always available")
	flag.DurationVar(&c.retention, "retention", purge.DefaultAge, "how long originals stay on this PC")
	flag.DurationVar(&c.autoSettle, "sim-auto-settle", 0, "with -payments=sim, settle a charge after this long with nobody pressing anything")
	flag.Float64Var(&c.speed, "sim-print-speed", 1, "with -printer=sim, divide the manufacturer's print time by this")
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
	if source == httpd.SourceHotFolder && c.hotFolder == "" {
		return errors.New("-source=hotfolder needs -hot-folder pointing at the camera's destination directory")
	}

	db, err := store.Open(filepath.Join(root, "booth.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	photos := photo.New(db)
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

	templates, err := loadTemplates(c.templates, log)
	if err != nil {
		return err
	}
	packages, err := catalog.All()
	if err != nil {
		return err
	}

	srv, err := httpd.New(httpd.Deps{
		Sessions: sessions, Photos: photos, Payments: payments, Printer: prints,
		Ingest: watcher, Templates: templates, Packages: packages,
		Root: root, Source: source, OutletID: c.outlet,
		Simulated: simulated, Log: log,
	})
	if err != nil {
		return err
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
	background("purge", purge.New(photos, root, c.retention, log).Run)

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
			"templates", len(templates), "packages", len(packages),
			"payments", providerName(provider), "printer", backend.Name())
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
	default:
		return "", fmt.Errorf(`unknown -source %q: want "hotfolder" or "webcam"`, s)
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
	default:
		// The real one is DNP's Windows driver and SDK. Named here so the error
		// says what is missing rather than only what is wrong.
		return nil, fmt.Errorf(`unknown -printer %q: want "sim" (the DNP DS-RX1HS backend is not built yet)`, c.printerKit)
	}
}

// loadTemplates returns the built-in designs plus anything in dir.
//
// A broken design in dir is reported and skipped rather than fatal: a booth
// that will not start because somebody dropped in a bad template.json is worse
// than one that starts with the designs that work.
func loadTemplates(dir string, log *slog.Logger) ([]compose.Template, error) {
	builtin, err := compose.Builtin()
	if err != nil {
		return nil, fmt.Errorf("built-in templates: %w", err)
	}

	extra, err := compose.LoadDir(dir)
	if err != nil {
		log.Error("template directory has a problem; the designs that loaded are still available",
			"dir", dir, "err", err)
	}

	seen := make(map[string]bool, len(builtin))
	out := make([]compose.Template, 0, len(builtin)+len(extra))
	for _, t := range builtin {
		seen[t.ID] = true
		out = append(out, t)
	}
	for _, t := range extra {
		if seen[t.ID] {
			// An outlet's own design wins over a built-in with the same name,
			// which is the only reason to give one the same name.
			for i := range out {
				if out[i].ID == t.ID {
					out[i] = t
				}
			}
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func providerName(p payment.Provider) string {
	if p == nil {
		return "disabled"
	}
	return p.Name()
}
