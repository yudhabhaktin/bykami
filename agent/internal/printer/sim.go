package printer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Simulated is the only backend that exists today.
//
// The real one is DNP's Windows driver and SDK, which needs the printer, the
// SDK download and a Windows box — none of which are on the critical path for
// getting the rest of the flow right. This one does everything the queue can
// observe: it takes the manufacturer's print time, it fails when asked, and it
// refuses a file that is not there.
//
// It is not a mode the booth can be left in by accident. cmd/bykami-agent
// requires -printer=sim explicitly and logs a warning at startup, in the same
// way api/ requires -otp-delivery=log for the sender that must never reach
// production.
type Simulated struct {
	log *slog.Logger

	// speed divides the manufacturer's print time. 1 is real time, which is
	// what a rehearsal of a real session wants; tests set it high.
	speed float64

	mu sync.Mutex
	// failNext makes the next job fail, so the failure path can be exercised
	// without unplugging anything.
	failNext error
	printed  []Job
}

func NewSimulated(log *slog.Logger, speed float64) *Simulated {
	if speed <= 0 {
		speed = 1
	}
	return &Simulated{log: log, speed: speed}
}

func (s *Simulated) Name() string { return "simulated" }

func (s *Simulated) Print(ctx context.Context, job Job, imagePath string) error {
	s.mu.Lock()
	fail := s.failNext
	s.failNext = nil
	s.mu.Unlock()

	if fail != nil {
		return fail
	}

	// A real printer cannot print a file that does not exist, and neither can
	// this: a compose step that silently produced nothing must surface as a
	// failed job rather than a successful one with no paper.
	if _, err := os.Stat(imagePath); err != nil {
		return fmt.Errorf("simulated printer: %w", err)
	}

	spec, ok := specs[job.Layout]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownLayout, job.Layout)
	}
	sheets := job.Sheets
	if spec.sheetCost > 0 {
		sheets = job.Sheets / spec.sheetCost
	}
	d := time.Duration(float64(spec.Duration) * float64(max(sheets, 1)) / s.speed)

	// cut is logged rather than acted on. There is no blade to drive here, and
	// the whole point of recording it on the job is that the real DNP backend
	// will have one instruction to read when it exists.
	s.log.Info("printer: simulating", "job", job.ID, "layout", job.Layout, "cut", job.Cut, "for", d)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
	}

	s.mu.Lock()
	s.printed = append(s.printed, job)
	s.mu.Unlock()
	return nil
}

// FailNext makes the next print attempt fail with err.
func (s *Simulated) FailNext(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = err
}

// Printed returns the jobs that came out, for tests and for the rehearsal
// script.
func (s *Simulated) Printed() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Job(nil), s.printed...)
}
