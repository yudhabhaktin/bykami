package printer

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The cut decides what the customer walks away holding, so a job that cannot
// have it must fail rather than come out of the other queue.
func TestQueueForRefusesTheOtherFormat(t *testing.T) {
	both := SpoolerConfig{Queue: "DS-RX1", CutQueue: "DS-RX1 cut"}

	for _, tc := range []struct {
		name string
		cfg  SpoolerConfig
		cut  bool
		want string
	}{
		{"whole sheet", both, false, "DS-RX1"},
		{"cut into strips", both, true, "DS-RX1 cut"},
	} {
		got, err := tc.cfg.queueFor(tc.cut)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: queue %q, want %q", tc.name, got, tc.want)
		}
	}

	if _, err := (SpoolerConfig{Queue: "DS-RX1"}).queueFor(true); err == nil {
		t.Error("a cut job on a booth with no cut queue printed a whole sheet instead of failing")
	}
	if _, err := (SpoolerConfig{CutQueue: "DS-RX1 cut"}).queueFor(false); err == nil {
		t.Error("a whole-sheet job on a booth with only a cut queue came out cut instead of failing")
	}
}

// GDI will stretch whatever it is given onto whatever page the driver is set
// to, so the mismatch has to be caught here or the customer catches it on
// paper, after the sheet is spent.
func TestPageFitsCatchesTheWrongPageSize(t *testing.T) {
	for _, tc := range []struct {
		name              string
		layout            Layout
		pageW, pageH, dpi int
		ok                bool
	}{
		{"4R on a 4x6 page", Layout4R, 1200, 1800, 300, true},
		{"4R with the bleed a borderless driver adds", Layout4R, 1236, 1836, 300, true},
		{"4R on a driver running at 600 dpi", Layout4R, 2400, 3600, 600, true},
		{"4R sent to a 6x8 page", Layout4R, 1800, 2400, 300, false},
		{"a 2x6 strip sent to a 4x6 page", LayoutStrip, 1200, 1800, 300, false},
		{"a 2x6 strip on a 2x6 page", LayoutStrip, 600, 1800, 300, true},
		{"6x8 on a 6x8 page", Layout6x8, 1800, 2400, 300, true},
		{"a page rotated to landscape", Layout4R, 1800, 1200, 300, false},
	} {
		spec, ok := SpecFor(tc.layout)
		if !ok {
			t.Fatalf("no spec for %q", tc.layout)
		}
		err := pageFits(spec, tc.pageW, tc.pageH, tc.dpi, tc.dpi)
		if tc.ok && err != nil {
			t.Errorf("%s: refused, want printed: %v", tc.name, err)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("%s: printed, want refused", tc.name)
				continue
			}
			if !errors.Is(err, ErrPageMismatch) {
				t.Errorf("%s: %v, want ErrPageMismatch", tc.name, err)
			}
			// The fix is at the machine, so the message has to say which page
			// size to select as well as which one is wrong.
			if !strings.Contains(err.Error(), "the queue is set to") {
				t.Errorf("%s: %q does not say what the queue is set to", tc.name, err)
			}
		}
	}
}

// A driver that reports no physical page is not a reason to refuse the print.
func TestPageFitsAllowsADriverThatReportsNothing(t *testing.T) {
	spec, _ := SpecFor(Layout4R)
	for _, tc := range []struct{ w, h, dpi int }{{0, 0, 300}, {1200, 1800, 0}, {-1, -1, -1}} {
		if err := pageFits(spec, tc.w, tc.h, tc.dpi, tc.dpi); err != nil {
			t.Errorf("page %dx%d at %d dpi: %v, want the print to go ahead", tc.w, tc.h, tc.dpi, err)
		}
	}
}

// Paper out at 9pm is a thirty-second fix, not a failed print.
func TestStalledSeparatesWhatAPersonCanFix(t *testing.T) {
	for _, tc := range []struct {
		status uint32
		want   bool
	}{
		{jobStatusPaperOut, true},
		{jobStatusOffline, true},
		{jobStatusUserIntervention, true},
		{jobStatusBlockedDevQ, true},
		{jobStatusPaused, true},
		{jobStatusPrinting, false},
		{jobStatusSpooling, false},
		{jobStatusError, false},
		{jobStatusPrinted, false},
		{0, false},
	} {
		if got := stalled(tc.status) != ""; got != tc.want {
			t.Errorf("status 0x%04x: stalled = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// finishFailed writes this into the job's error column, and "it failed" is not
// actionable at an event with a queue of people waiting.
func TestDescribeStatusPrefersTheSpoolersOwnWords(t *testing.T) {
	if got := describeStatus(jobStatusError, "Out of paper"); got != "Out of paper" {
		t.Errorf("got %q, want the spooler's own text", got)
	}
	if got := describeStatus(jobStatusError, "   "); got == "" || strings.TrimSpace(got) == "" {
		t.Errorf("blank spooler text left the error column empty")
	}
	if got := describeStatus(jobStatusPaperOut, ""); got != "out of paper" {
		t.Errorf("got %q, want the condition named", got)
	}
	if got := describeStatus(0x8000, ""); !strings.Contains(got, "0x8000") {
		t.Errorf("got %q, want the raw status when nothing else is known", got)
	}
}

func TestNewSpoolerNeedsAQueue(t *testing.T) {
	_, err := NewSpooler(SpoolerConfig{}, quietLog())
	if err == nil {
		t.Fatal("a spooler with no queue name at all started")
	}
	// Checked before the platform, so the message names the real mistake on a
	// developer's laptop as well as on the booth PC.
	if !strings.Contains(err.Error(), "queue") {
		t.Errorf("%q does not say a queue name is missing", err)
	}
}
