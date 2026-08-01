package printer_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/printer"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

func setup(t *testing.T) (*sql.DB, *printer.Queue, *printer.Simulated, string) {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(
		`INSERT INTO sessions
		   (id, outlet_id, state, package_id, package_name, price_idr, template_id,
		    print_copies, take_limit, opened_at, paid_at)
		 VALUES ('s1', 'jajag', 'open', 'mini', 'MINI', 45000, 'strip-3', 1, 15, unixepoch(), unixepoch())`,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Fast enough that the tests are not sleeping through 12.4 seconds a sheet.
	backend := printer.NewSimulated(slog.New(slog.DiscardHandler), 10000)
	q := printer.New(db, backend, slog.New(slog.DiscardHandler))

	sheet := filepath.Join(t.TempDir(), "composed.jpg")
	if err := os.WriteFile(sheet, []byte("pretend this is a composed sheet"), 0o644); err != nil {
		t.Fatalf("write sheet: %v", err)
	}
	return db, q, backend, sheet
}

// drain runs the queue until nothing is pending, then stops it. Every test
// wants "print what I submitted and come back", not a long-lived worker.
func drain(t *testing.T, q *printer.Queue, sheet string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Run(ctx, func(j printer.Job) (string, error) {
			if j.SheetPath == "" {
				return "", errors.New("job carries no sheet path")
			}
			return sheet, nil
		})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for pending(t, q) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the queue did not drain")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

func pending(t *testing.T, q *printer.Queue) int {
	t.Helper()
	jobs, err := q.BySession(context.Background(), "s1")
	if err != nil {
		t.Fatalf("by session: %v", err)
	}
	n := 0
	for _, j := range jobs {
		if j.State == printer.Queued || j.State == printer.Printing {
			n++
		}
	}
	return n
}

// Two strips come off one 4x6 sheet. Counting copies instead of sheets would
// make the roll appear to last half as long as it does.
func TestSheetsPerLayout(t *testing.T) {
	for _, tc := range []struct {
		layout printer.Layout
		copies int
		want   int
	}{
		{printer.Layout4R, 1, 1},
		{printer.Layout4R, 3, 3},
		{printer.LayoutStrip, 1, 1}, // one strip still costs a whole sheet
		{printer.LayoutStrip, 2, 1}, // the native 2-inch cut
		{printer.LayoutStrip, 3, 2},
		{printer.LayoutStrip, 400, 200}, // "Limited Print — 400 strip" is 200 sheets
		{printer.Layout6x8, 1, 2},       // twice the area, twice the roll
	} {
		spec, ok := printer.SpecFor(tc.layout)
		if !ok {
			t.Fatalf("no spec for %q", tc.layout)
		}
		if got := spec.Sheets(tc.copies); got != tc.want {
			t.Errorf("%s x%d = %d sheets, want %d", tc.layout, tc.copies, got, tc.want)
		}
	}
}

// One roll is 700 sheets, which is 1,400 strips. This is the arithmetic behind
// the second-roll rule for long Unlimited Print bookings.
func TestRollCoversTheBoothCatalogue(t *testing.T) {
	spec, _ := printer.SpecFor(printer.LayoutStrip)
	if got := spec.Sheets(200); got != 100 {
		t.Fatalf("200 strips = %d sheets, want 100", got)
	}
	if printer.RollSheets != 700 {
		t.Fatalf("roll = %d sheets, want 700", printer.RollSheets)
	}
}

func TestPrintConsumesMedia(t *testing.T) {
	_, q, backend, sheet := setup(t)
	ctx := context.Background()

	if err := q.LoadRoll(ctx, printer.RollSheets, "roll 1"); err != nil {
		t.Fatalf("load roll: %v", err)
	}
	if _, err := q.Submit(ctx, "s1", printer.LayoutStrip, 4, true, "sheets/s1/composed.jpg"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	drain(t, q, sheet)

	if n := len(backend.Printed()); n != 1 {
		t.Fatalf("backend printed %d jobs, want 1", n)
	}
	remaining, err := q.Remaining(ctx)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	// Four strips is two sheets.
	if want := printer.RollSheets - 2; remaining != want {
		t.Fatalf("remaining = %d, want %d", remaining, want)
	}
}

// The signal a browser could never give: better to refuse now than to stop
// halfway through a customer's strip.
func TestSubmitRefusesWhenTheRollCannotCoverIt(t *testing.T) {
	_, q, _, _ := setup(t)
	ctx := context.Background()

	if err := q.LoadRoll(ctx, 3, "nearly empty"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := q.Submit(ctx, "s1", printer.Layout4R, 4, true, "sheets/s1/composed.jpg"); !errors.Is(err, printer.ErrNoMedia) {
		t.Fatalf("want ErrNoMedia, got %v", err)
	}
	// And what does fit is still accepted.
	if _, err := q.Submit(ctx, "s1", printer.Layout4R, 3, true, "sheets/s1/composed.jpg"); err != nil {
		t.Fatalf("a job that fits was refused: %v", err)
	}
}

// Queued-but-unprinted sheets count against the roll, or two jobs that each fit
// individually can be accepted when together they do not.
func TestQueuedSheetsAreCountedAgainstTheRoll(t *testing.T) {
	_, q, _, _ := setup(t)
	ctx := context.Background()

	if err := q.LoadRoll(ctx, 4, "roll"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := q.Submit(ctx, "s1", printer.Layout4R, 3, true, "sheets/s1/composed.jpg"); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if _, err := q.Submit(ctx, "s1", printer.Layout4R, 3, true, "sheets/s1/composed.jpg"); !errors.Is(err, printer.ErrNoMedia) {
		t.Fatalf("second submit ignored the queue: %v", err)
	}
}

func TestFailedJobRecordsWhyAndConsumesNothing(t *testing.T) {
	_, q, backend, sheet := setup(t)
	ctx := context.Background()

	if err := q.LoadRoll(ctx, 100, "roll"); err != nil {
		t.Fatalf("load: %v", err)
	}
	backend.FailNext(errors.New("media door open"))

	job, err := q.Submit(ctx, "s1", printer.Layout4R, 1, true, "sheets/s1/composed.jpg")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	drain(t, q, sheet)

	got, err := q.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != printer.Failed {
		t.Fatalf("state = %q, want failed", got.State)
	}
	if got.Error == "" {
		t.Fatal("a failed job with no reason is not actionable")
	}

	// Over-counting the roll is the safer error: it makes the operator load
	// media early rather than run out mid-session.
	remaining, err := q.Remaining(ctx)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	if remaining != 100 {
		t.Fatalf("a failed sheet consumed media: remaining = %d, want 100", remaining)
	}
}

func TestMissingImageFailsTheJob(t *testing.T) {
	_, q, _, _ := setup(t)
	ctx := context.Background()

	if err := q.LoadRoll(ctx, 100, "roll"); err != nil {
		t.Fatalf("load: %v", err)
	}
	job, err := q.Submit(ctx, "s1", printer.Layout4R, 1, true, "sheets/s1/composed.jpg")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	drain(t, q, filepath.Join(t.TempDir(), "does-not-exist.jpg"))

	got, err := q.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != printer.Failed {
		t.Fatal("a job with no image to print reported success")
	}
}

func TestJobsPrintInOrder(t *testing.T) {
	_, q, backend, sheet := setup(t)
	ctx := context.Background()

	if err := q.LoadRoll(ctx, 100, "roll"); err != nil {
		t.Fatalf("load: %v", err)
	}
	var ids []string
	for range 3 {
		j, err := q.Submit(ctx, "s1", printer.Layout4R, 1, true, "sheets/s1/composed.jpg")
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		ids = append(ids, j.ID)
		// queued_at has one-second resolution, so order within a second falls
		// back to the id. Nudge the clock instead of asserting on a tie.
		time.Sleep(1100 * time.Millisecond)
	}

	drain(t, q, sheet)

	printed := backend.Printed()
	if len(printed) != 3 {
		t.Fatalf("printed %d jobs, want 3", len(printed))
	}
	for i, want := range ids {
		if printed[i].ID != want {
			t.Fatalf("job %d was %s, want %s", i, printed[i].ID, want)
		}
	}
}

// A sheet either came out or it did not, and a restarted process cannot tell
// which — so it says so rather than reprinting silently.
func TestRestartFailsInterruptedJobs(t *testing.T) {
	db, q, _, sheet := setup(t)
	ctx := context.Background()

	if err := q.LoadRoll(ctx, 100, "roll"); err != nil {
		t.Fatalf("load: %v", err)
	}
	job, err := q.Submit(ctx, "s1", printer.Layout4R, 1, true, "sheets/s1/composed.jpg")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// The state a crash mid-print leaves behind.
	if _, err := db.Exec(`UPDATE print_jobs SET state = 'printing', started_at = unixepoch() WHERE id = ?`, job.ID); err != nil {
		t.Fatalf("simulate crash: %v", err)
	}

	drain(t, q, sheet)

	got, err := q.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != printer.Failed {
		t.Fatalf("state = %q, want failed", got.State)
	}
	if got.Error == "" {
		t.Fatal("no explanation for the operator deciding whether to reprint")
	}
}

func TestMediaAdjustmentNeedsAReason(t *testing.T) {
	_, q, _, _ := setup(t)
	ctx := context.Background()

	if err := q.AdjustMedia(ctx, -5, ""); err == nil {
		t.Fatal("accepted an unexplained adjustment")
	}
	if err := q.AdjustMedia(ctx, -5, "jam, five sheets wasted"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	remaining, err := q.Remaining(ctx)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	if remaining != -5 {
		t.Fatalf("remaining = %d, want -5", remaining)
	}
}

func TestSubmitRejectsAnUnknownLayout(t *testing.T) {
	_, q, _, _ := setup(t)
	ctx := context.Background()

	if err := q.LoadRoll(ctx, 100, "roll"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := q.Submit(ctx, "s1", printer.Layout("polaroid"), 1, true, "sheets/s1/composed.jpg"); !errors.Is(err, printer.ErrUnknownLayout) {
		t.Fatalf("want ErrUnknownLayout, got %v", err)
	}
}

// A job with nothing to print is refused at submit rather than discovered by
// the worker, because a queued job that can only fail has already taken media
// out of the customer's budget in the check above.
func TestSubmitRequiresAComposedSheet(t *testing.T) {
	_, q, _, _ := setup(t)
	ctx := context.Background()

	if err := q.LoadRoll(ctx, 100, "roll"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := q.Submit(ctx, "s1", printer.Layout4R, 1, true, ""); err == nil {
		t.Fatal("queued a job with no image")
	}
}

// The path survives a restart, which is the reason it is a column rather than
// a map in memory: a queued job outlives the process that queued it.
func TestSheetPathIsPersisted(t *testing.T) {
	_, q, _, _ := setup(t)
	ctx := context.Background()

	if err := q.LoadRoll(ctx, 100, "roll"); err != nil {
		t.Fatalf("load: %v", err)
	}
	job, err := q.Submit(ctx, "s1", printer.Layout4R, 1, true, "sheets/s1/a.jpg")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	got, err := q.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SheetPath != "sheets/s1/a.jpg" {
		t.Fatalf("sheet path = %q", got.SheetPath)
	}
}
