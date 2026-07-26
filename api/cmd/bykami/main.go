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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/identity"
	"github.com/bhaktiyudha/bykami/api/internal/loyalty"
	"github.com/bhaktiyudha/bykami/api/internal/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address; localhost because Cloudflare Tunnel dials out to it")
	dsn := flag.String("db", "bykami.db", "path to the SQLite database")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(*addr, *dsn, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(addr, dsn string, log *slog.Logger) error {
	db, err := store.Open(dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	// Wired here and nowhere else. Modules take their dependencies as
	// parameters, which is what keeps the boundaries real rather than aspirational.
	_ = identity.New(db, logSender{log})
	_ = loyalty.New(db)

	mux := http.NewServeMux()

	// Readiness, not liveness: it touches the database, because a process that
	// is running but cannot reach its own storage is not ready to take traffic.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			log.Error("health check failed", "err", err)
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
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
		log.Info("listening", "addr", addr, "db", dsn)
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

// logSender is the placeholder delivery channel. WhatsApp is the intended
// primary and needs a provider account that does not exist yet; until it does,
// codes go to the log so the flow is exercisable end to end in development.
//
// It must never reach production: a one-time code in a log file is a one-time
// code in whatever reads that log.
type logSender struct{ log *slog.Logger }

func (s logSender) Send(_ context.Context, e164, code string) error {
	s.log.Warn("OTP delivery is not configured; code written to log", "phone", e164, "code", code)
	return nil
}
