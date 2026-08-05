package printer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Spooler prints through the Windows print spooler, which is what drives the
// DNP DS-RX1HS on the booth PC.
//
// Nothing in this file names the machine, and that is the point: the
// DNP-specific part is the driver Windows already has, so the booth learns no
// vendor SDK here for the same reason it learns no camera SDK in
// internal/shutter. What it does learn is how to ask the spooler what became of
// a document, which is the half window.print() cannot do and the reason this
// package exists at all.
//
// Two things it refuses to do, both of which cost media when they go wrong:
// print a layout the queue's page size does not match, and walk away from a
// document it has already reported as failed.
type Spooler struct {
	cfg SpoolerConfig
	log *slog.Logger
}

// SpoolerConfig is the per-machine half of printing: which Windows queue, and
// how long to wait for it.
type SpoolerConfig struct {
	// Queue is the Windows print queue for sheets that come out whole.
	Queue string

	// CutQueue is a second queue on the same printer, whose driver has the
	// 2-inch cut turned on.
	//
	// Two queues rather than one, because the cut is a DEVMODE setting and the
	// customer chooses it per session — they decide between two 2x6 strips and
	// one 4x6 kept whole before they pick a frame. Setting DEVMODE per job
	// means the driver's private extension, which is exactly the vendor SDK
	// this design avoids; adding a second queue against the same device is a
	// right-click in Windows, and it leaves the setting somewhere the person
	// standing at the machine can see it.
	CutQueue string

	// Wait is how long past the expected print time a document may sit in the
	// spooler before it is given up on and cancelled. It is the window an
	// operator has to load paper, clear a jam or bring the printer back online
	// without the customer losing their print.
	Wait time.Duration

	// Poll is how often the spooler is asked what happened.
	Poll time.Duration
}

const (
	// DefaultSpoolWait is roughly ten sheets of slack past the print time —
	// long enough to walk to the machine, open it and load a roll, and short
	// enough that a booth left alone with a dead printer eventually says so
	// instead of holding the queue forever.
	DefaultSpoolWait = 2 * time.Minute

	// DefaultSpoolPoll is well under the 12.4 s a sheet takes, so the answer
	// arrives about when the sheet does.
	DefaultSpoolPoll = 500 * time.Millisecond
)

// sheetDPI is the resolution Spec's pixel dimensions are quoted at.
const sheetDPI = 300

// pageTolerance is how far the driver's page may sit from the layout before a
// job is refused, as a fraction.
//
// Not zero, because a dye-sub prints past the edge on purpose: a borderless
// 4x6 page measures a little over 4x6 so the image can bleed, and the exact
// overprint is the driver's business. Wide enough to absorb that, nowhere near
// wide enough to confuse 4x6 with 2x6 or 6x8, which are the mistakes that
// matter.
const pageTolerance = 0.08

// ErrPageMismatch is a job whose layout is not what the queue is set up to
// print.
//
// Worth its own error because the alternative is silent: GDI will happily
// stretch a 2x6 strip across a 4x6 sheet, and nobody finds out until a customer
// is handed a distorted print that has already cost a sheet of media.
var ErrPageMismatch = errors.New("printer: the queue's page size does not match this layout")

// NewSpooler prepares the backend, and fails here rather than at the first
// print. Everything it checks is a deployment mistake, and a deployment mistake
// found at startup is a service that will not come up — versus one found at the
// first print, which is a customer who has already paid.
func NewSpooler(cfg SpoolerConfig, log *slog.Logger) (*Spooler, error) {
	if cfg.Queue == "" && cfg.CutQueue == "" {
		return nil, errors.New("printer: the spooler backend needs at least one Windows queue name")
	}
	if cfg.Wait <= 0 {
		cfg.Wait = DefaultSpoolWait
	}
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultSpoolPoll
	}
	if err := spoolerSupported(); err != nil {
		return nil, err
	}

	// Only the names have been checked so far, and a name is exactly the thing
	// that gets typed wrong in a service definition. Without this the booth
	// starts, sells a session, takes the money and only then finds it has
	// nowhere to print.
	//
	// Windows keeps a print queue whether or not the printer is plugged in or
	// switched on, which is what makes this safe to be fatal: a queue that does
	// not exist is a deployment mistake and should stop the booth coming up,
	// while a printer that is merely off is an operational state the spooler
	// wait and the media ledger already handle without anybody restarting
	// anything.
	for _, q := range []string{cfg.Queue, cfg.CutQueue} {
		if q == "" {
			continue
		}
		if err := probeQueue(q); err != nil {
			return nil, err
		}
	}

	// A missing queue is not fatal — a booth that only ever sells one of the
	// two formats is a real configuration — but it is silent until somebody
	// buys the other one, so it is said out loud now.
	if cfg.CutQueue == "" {
		log.Warn("printer: no cut queue configured; a customer who chooses two strips will get a failed print", "queue", cfg.Queue)
	}
	if cfg.Queue == "" {
		log.Warn("printer: no uncut queue configured; a customer who chooses a whole 4x6 will get a failed print", "cut_queue", cfg.CutQueue)
	}
	log.Info("printer: printing through the Windows spooler", "queue", cfg.Queue, "cut_queue", cfg.CutQueue, "wait", cfg.Wait)

	return &Spooler{cfg: cfg, log: log}, nil
}

func (s *Spooler) Name() string { return "windows-spooler" }

// Print renders the composed sheet to the queue and blocks until the spooler
// says the document is out of the machine.
func (s *Spooler) Print(ctx context.Context, job Job, imagePath string) error {
	spec, ok := specs[job.Layout]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownLayout, job.Layout)
	}
	queue, err := s.cfg.queueFor(job.Cut)
	if err != nil {
		return err
	}

	// Pages, not copies. Two strips come off one fed sheet, so a two-copy strip
	// job is one page through the machine and the driver's cut makes the pair.
	pages := spec.Fed(job.Copies)
	if pages <= 0 {
		return errors.New("printer: nothing to feed")
	}

	return s.spool(ctx, spoolJob{
		queue:  queue,
		name:   "bykami " + job.ID,
		spec:   spec,
		pages:  pages,
		image:  imagePath,
		poll:   s.cfg.Poll,
		budget: time.Duration(pages)*spec.Duration + s.cfg.Wait,
	})
}

// spoolJob is one document, as the platform half needs it.
type spoolJob struct {
	queue  string
	name   string
	spec   Spec
	pages  int
	image  string
	poll   time.Duration
	budget time.Duration
}

// queueFor picks the queue by whether the blade is wanted.
//
// It refuses rather than falling back to the other queue. The cut is what the
// customer walks away holding — two strips or one whole 4x6 — so printing the
// other one is not a degraded result, it is the wrong product on paid media.
func (c SpoolerConfig) queueFor(cut bool) (string, error) {
	if cut {
		if c.CutQueue == "" {
			return "", errors.New("printer: this job asks for the 2-inch cut and no cut queue is configured")
		}
		return c.CutQueue, nil
	}
	if c.Queue == "" {
		return "", errors.New("printer: this job asks for a whole sheet and no uncut queue is configured")
	}
	return c.Queue, nil
}

// pageFits compares the driver's page against the layout, in inches.
//
// Inches rather than pixels because the two sides are quoted differently: a
// Spec is pixels at 300 dpi, and a driver reports device units at whatever
// resolution it runs. Physical size is the only comparison that means the same
// thing to both.
//
// A driver that reports nothing is not an error. Some report no physical page
// at all, and refusing to print on that basis would trade a real failure for a
// hypothetical one — the caller logs it and prints.
func pageFits(spec Spec, pageW, pageH, dpiX, dpiY int) error {
	if pageW <= 0 || pageH <= 0 || dpiX <= 0 || dpiY <= 0 {
		return nil
	}

	wantW, wantH := float64(spec.WidthPx)/sheetDPI, float64(spec.HeightPx)/sheetDPI
	gotW, gotH := float64(pageW)/float64(dpiX), float64(pageH)/float64(dpiY)

	if within(gotW, wantW) && within(gotH, wantH) {
		return nil
	}
	// Both sizes in the message, because the fix is at the machine and the
	// person making it needs to know which page size to select in the driver.
	return fmt.Errorf("%w: %s needs %.2f x %.2f in, the queue is set to %.2f x %.2f in",
		ErrPageMismatch, spec.Layout, wantW, wantH, gotW, gotH)
}

func within(got, want float64) bool {
	if want <= 0 {
		return false
	}
	d := got - want
	if d < 0 {
		d = -d
	}
	return d/want <= pageTolerance
}

// What the spooler reports about a job. These are the bits of JOB_INFO_1's
// Status field, which is protocol rather than an API binding, so they live here
// with the code that reads them rather than in the Windows file.
const (
	jobStatusPaused           = 0x0001
	jobStatusError            = 0x0002
	jobStatusDeleting         = 0x0004
	jobStatusSpooling         = 0x0008
	jobStatusPrinting         = 0x0010
	jobStatusOffline          = 0x0020
	jobStatusPaperOut         = 0x0040
	jobStatusPrinted          = 0x0080
	jobStatusDeleted          = 0x0100
	jobStatusBlockedDevQ      = 0x0200
	jobStatusUserIntervention = 0x0400
	jobStatusComplete         = 0x1000
)

// stalled names the conditions a person standing at the machine can clear, and
// returns empty for everything else.
//
// These are waited through rather than failed on, and the difference is the
// whole customer experience: running out of paper is a thirty-second fix, and a
// booth that failed the print the instant the roll ended would have taken money
// and told somebody to call staff for a problem that was already solved by the
// time they got there.
func stalled(status uint32) string {
	switch {
	case status&jobStatusPaperOut != 0:
		return "out of paper"
	case status&jobStatusOffline != 0:
		return "the printer is offline"
	case status&jobStatusUserIntervention != 0:
		return "the printer wants attention"
	case status&jobStatusBlockedDevQ != 0:
		return "the queue is blocked"
	case status&jobStatusPaused != 0:
		return "the job is paused"
	}
	return ""
}

// describeStatus turns what the spooler said into something worth putting in a
// job's error column, which finishFailed insists is actionable.
//
// The spooler's own text wins when there is any — "Out of paper" beats any
// wording invented here, and it is localised to whatever the machine speaks.
// The bit names are the fallback for drivers that supply none.
func describeStatus(status uint32, text string) string {
	if t := strings.TrimSpace(text); t != "" {
		return t
	}
	if s := stalled(status); s != "" {
		return s
	}

	var named []string
	for _, s := range []struct {
		bit  uint32
		name string
	}{
		{jobStatusError, "the spooler reported an error"},
		{jobStatusDeleting, "the job is being cancelled"},
		{jobStatusDeleted, "the job was cancelled"},
		{jobStatusSpooling, "still spooling"},
		{jobStatusPrinting, "still printing"},
	} {
		if status&s.bit != 0 {
			named = append(named, s.name)
		}
	}
	if len(named) == 0 {
		return fmt.Sprintf("the spooler said nothing beyond status 0x%04x", status)
	}
	return strings.Join(named, ", ")
}
