//go:build windows

package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/bhaktiyudha/bykami/agent/internal/update"
	"golang.org/x/sys/windows/svc"
)

// agentService implements svc.Handler so the booth can run under the Windows
// Service Control Manager. It starts the booth in a goroutine, reports
// SERVICE_RUNNING, and shuts down cleanly on Stop or Shutdown.
//
// This has never run on a real Windows machine.
type agentService struct {
	c      config
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *agentService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	s.log.Info("service starting")
	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})

	go func() {
		defer close(s.done)
		if err := runCtx(ctx, s.c, s.log); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Error("service run ended with error", "err", err)
		}
	}()

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	s.log.Info("service running")

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				s.log.Info("service stopping", "cmd", c.Cmd)
				cancel()
				<-s.done
				s.log.Info("service stopped")
				return false, 0
			}
		}
	}
}

func runAsService(c config, log *slog.Logger) error {
	return svc.Run(update.ServiceName, &agentService{c: c, log: log})
}
