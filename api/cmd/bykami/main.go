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
	"syscall"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/httpapi"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
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
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(*addr, *dsn, *otpDelivery, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(addr, dsn, otpDelivery string, log *slog.Logger) error {
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
	handler := httpapi.New(
		identity.New(db, sender),
		loyalty.New(db),
		db.PingContext,
		log,
		authEnabled,
	)

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
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

// logSender is the placeholder delivery channel. WhatsApp is the intended
// primary and needs a provider account that does not exist yet; until it does,
// codes go to the log so the flow is exercisable end to end in development.
//
// It must never reach production: a one-time code in a log file is a one-time
// code in whatever reads that log. That is enforced rather than remembered —
// reaching it requires -otp-delivery=log, which the systemd unit does not pass.
type logSender struct{ log *slog.Logger }

func (s logSender) Send(_ context.Context, e164, code string) error {
	s.log.Warn("OTP delivery is not configured; code written to log", "phone", e164, "code", code)
	return nil
}
