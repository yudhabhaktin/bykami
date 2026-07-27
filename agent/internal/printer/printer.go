// Package printer owns the print queue, because a browser cannot.
//
// window.print() is fire-and-forget by design: no job status, no error, no
// media remaining. Running out of media mid-session with no signal is the
// failure that loses a customer, and it is the whole reason this package exists
// rather than a button in the kiosk UI.
//
// The hardware is a DNP DS-RX1HS. Its numbers are not configuration — they are
// facts about the machine, verified in design/kiosk.md, and they belong in code
// where the media counter can use them.
package printer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// RollSheets is one roll of media, in 4×6 sheets.
//
// The constraint behind an operational rule the price list does not state: at
// 12.4 s a sheet, continuous printing exhausts a roll in about 2.4 hours, so
// any Unlimited Print booking of three hours or more ships with a second roll.
// That is a packing-list item; this counter is what makes it actionable
// mid-event rather than discovered when the printer stops.
const RollSheets = 700

type Layout string

const (
	// Layout4R is the studio's output: one image, 4×6.
	Layout4R Layout = "4r"

	// LayoutStrip is the booth format. The printer's native 2-inch cut yields
	// two strips from one 4×6 sheet, which is why 400 strips is 200 sheets.
	LayoutStrip Layout = "strip2x6"

	Layout6x8 Layout = "6x8"
)

// Spec is what the machine does with a layout. Pixel dimensions are at 300 dpi,
// which is the target the resolution table in design/kiosk.md is measured
// against.
type Spec struct {
	Layout   Layout
	WidthPx  int
	HeightPx int
	// Duration is the manufacturer's print time for one sheet.
	Duration time.Duration
	// perSheet is how many copies come off one fed sheet.
	perSheet int
	// sheetCost is what one fed sheet costs in 4×6 units, which is what the
	// roll is counted in. 6×8 is twice the area and therefore twice the cost.
	sheetCost int
}

var specs = map[Layout]Spec{
	Layout4R:    {Layout: Layout4R, WidthPx: 1200, HeightPx: 1800, Duration: 12400 * time.Millisecond, perSheet: 1, sheetCost: 1},
	LayoutStrip: {Layout: LayoutStrip, WidthPx: 600, HeightPx: 1800, Duration: 12400 * time.Millisecond, perSheet: 2, sheetCost: 1},
	Layout6x8:   {Layout: Layout6x8, WidthPx: 1800, HeightPx: 2400, Duration: 22 * time.Second, perSheet: 1, sheetCost: 2},
}

// SpecFor returns the machine's behaviour for a layout.
func SpecFor(l Layout) (Spec, bool) {
	s, ok := specs[l]
	return s, ok
}

// Sheets is the media cost of n copies, in 4×6 units.
//
// Not the same as the number of copies, and the difference is the point: two
// strips come off one sheet, so counting copies would make the roll appear to
// last half as long as it does.
func (s Spec) Sheets(copies int) int {
	if copies <= 0 {
		return 0
	}
	fed := (copies + s.perSheet - 1) / s.perSheet
	return fed * s.sheetCost
}

var (
	ErrUnknownLayout = errors.New("printer: unknown layout")
	ErrNoMedia       = errors.New("printer: not enough media on the roll")
	ErrNotFound      = errors.New("printer: job not found")
)

type State string

const (
	Queued   State = "queued"
	Printing State = "printing"
	Done     State = "done"
	Failed   State = "failed"
)

type Job struct {
	ID        string
	SessionID string
	Layout    Layout
	// SheetPath is the composed image, relative to the store root.
	SheetPath  string
	Copies     int
	Sheets     int
	State      State
	Error      string
	QueuedAt   time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

// Backend is the machine. One method, because the queue owns everything else —
// retries, state, and the media ledger — and a backend that also owned those
// would be a second place for them to disagree.
type Backend interface {
	// Name identifies the driver in logs and in the admin view.
	Name() string
	// Print blocks until the sheet is out or the attempt has failed.
	Print(ctx context.Context, job Job, imagePath string) error
}

// Queue is the print queue and the media ledger together.
type Queue struct {
	db      *sql.DB
	backend Backend
	log     *slog.Logger
	now     func() time.Time

	// wake is how Submit tells a sleeping worker there is work, without the
	// worker polling. Buffered by one: a wake that arrives while another is
	// pending is redundant, not lost.
	wake chan struct{}

	// mu guards the media check-and-reserve. SQLite serialises the writes, but
	// two callers could both read "3 sheets left" and both queue a 2-sheet job.
	mu sync.Mutex
}

func New(db *sql.DB, backend Backend, log *slog.Logger) *Queue {
	return &Queue{db: db, backend: backend, log: log, now: time.Now, wake: make(chan struct{}, 1)}
}

// NewWithClock is for tests that assert on job timing.
func NewWithClock(db *sql.DB, backend Backend, log *slog.Logger, now func() time.Time) *Queue {
	q := New(db, backend, log)
	q.now = now
	return q
}

// Submit queues a print and reserves nothing — media is decremented when the
// sheet is actually printed, not when it is asked for.
//
// It does refuse up front when the roll cannot cover the queue, which is the
// signal the browser could never give: better to tell the customer now than to
// stop halfway through their strip.
func (q *Queue) Submit(ctx context.Context, sessionID string, layout Layout, copies int, sheetPath string) (Job, error) {
	spec, ok := specs[layout]
	if !ok {
		return Job{}, fmt.Errorf("%w: %q", ErrUnknownLayout, layout)
	}
	if copies <= 0 {
		return Job{}, errors.New("printer: copies must be positive")
	}
	if sheetPath == "" {
		return Job{}, errors.New("printer: a job needs a composed sheet to print")
	}
	sheets := spec.Sheets(copies)

	q.mu.Lock()
	defer q.mu.Unlock()

	remaining, err := q.Remaining(ctx)
	if err != nil {
		return Job{}, err
	}
	pending, err := q.pendingSheets(ctx)
	if err != nil {
		return Job{}, err
	}
	if remaining-pending < sheets {
		return Job{}, fmt.Errorf("%w: %d left, %d already queued, %d needed",
			ErrNoMedia, remaining, pending, sheets)
	}

	job := Job{
		ID:        newID(),
		SessionID: sessionID,
		Layout:    layout,
		Copies:    copies,
		Sheets:    sheets,
		SheetPath: sheetPath,
		State:     Queued,
		QueuedAt:  q.now(),
	}
	if _, err := q.db.ExecContext(ctx,
		`INSERT INTO print_jobs (id, session_id, layout, sheet_path, copies, sheets, state, queued_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'queued', ?)`,
		job.ID, job.SessionID, string(job.Layout), job.SheetPath, job.Copies, job.Sheets, job.QueuedAt.Unix(),
	); err != nil {
		return Job{}, fmt.Errorf("printer: submit: %w", err)
	}

	select {
	case q.wake <- struct{}{}:
	default:
	}
	return job, nil
}

// Run drains the queue until ctx is cancelled. One job at a time: there is one
// printer, and a second concurrent job would only interleave sheets.
func (q *Queue) Run(ctx context.Context, imagePath func(Job) (string, error)) error {
	// Anything left mid-flight by a crash is failed rather than resumed. The
	// sheet either came out or it did not, and this process cannot tell which —
	// so it says so instead of guessing, and a human reprints.
	if err := q.failInterrupted(ctx); err != nil {
		q.log.Error("printer: reconcile interrupted jobs", "err", err)
	}

	// A slow tick as well as the wake channel, so a job submitted by another
	// process — or one this process failed to wake for — is still picked up.
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()

	for {
		for {
			worked, err := q.step(ctx, imagePath)
			if err != nil {
				q.log.Error("printer: step", "err", err)
			}
			if !worked || ctx.Err() != nil {
				break
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-q.wake:
		case <-t.C:
		}
	}
}

// step prints at most one job and reports whether it did any work.
func (q *Queue) step(ctx context.Context, imagePath func(Job) (string, error)) (bool, error) {
	job, err := q.claim(ctx)
	switch {
	case errors.Is(err, ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}

	path, err := imagePath(job)
	if err != nil {
		return true, q.finishFailed(ctx, job, err)
	}

	q.log.Info("printer: printing", "job", job.ID, "layout", job.Layout,
		"copies", job.Copies, "sheets", job.Sheets, "backend", q.backend.Name())

	if err := q.backend.Print(ctx, job, path); err != nil {
		return true, q.finishFailed(ctx, job, err)
	}
	return true, q.finishDone(ctx, job)
}

// claim takes the oldest queued job and marks it printing in one statement, so
// two workers cannot claim the same job.
func (q *Queue) claim(ctx context.Context) (Job, error) {
	now := q.now()
	res, err := q.db.ExecContext(ctx,
		`UPDATE print_jobs SET state = 'printing', started_at = ?
		  WHERE id = (SELECT id FROM print_jobs WHERE state = 'queued' ORDER BY queued_at, id LIMIT 1)`,
		now.Unix(),
	)
	if err != nil {
		return Job{}, fmt.Errorf("printer: claim: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Job{}, ErrNotFound
	}
	return q.scanOne(ctx, `SELECT `+columns+` FROM print_jobs WHERE state = 'printing' ORDER BY started_at DESC LIMIT 1`)
}

// finishDone marks the job done and consumes the media in one transaction.
//
// Together or not at all: a counter that can disagree with the queue is the
// counter that reads 200 when the roll is empty.
func (q *Queue) finishDone(ctx context.Context, job Job) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("printer: finish: %w", err)
	}
	defer tx.Rollback()

	now := q.now().Unix()
	if _, err := tx.ExecContext(ctx,
		`UPDATE print_jobs SET state = 'done', finished_at = ? WHERE id = ?`, now, job.ID,
	); err != nil {
		return fmt.Errorf("printer: finish: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO media_entries (id, kind, sheets, job_id, created_at)
		 VALUES (?, 'consume', ?, ?, ?)`,
		newID(), -job.Sheets, job.ID, now,
	); err != nil {
		return fmt.Errorf("printer: consume media: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("printer: finish: %w", err)
	}

	remaining, err := q.Remaining(ctx)
	if err == nil && remaining <= lowMediaWarning {
		q.log.Warn("printer: media running low", "sheets_remaining", remaining)
	}
	q.log.Info("printer: done", "job", job.ID, "sheets_remaining", remaining)
	return nil
}

// lowMediaWarning is roughly ten minutes of continuous printing at 12.4 s a
// sheet — enough warning to fetch the second roll without stopping.
const lowMediaWarning = 50

// finishFailed records why. "It failed" is not actionable at 9pm at an event
// with a queue of people waiting, which is why the schema refuses it.
func (q *Queue) finishFailed(ctx context.Context, job Job, cause error) error {
	q.log.Error("printer: job failed", "job", job.ID, "err", cause)
	_, err := q.db.ExecContext(ctx,
		`UPDATE print_jobs SET state = 'failed', error = ?, finished_at = ? WHERE id = ?`,
		cause.Error(), q.now().Unix(), job.ID,
	)
	if err != nil {
		return fmt.Errorf("printer: record failure: %w", err)
	}
	// No media consumed: a failed sheet may or may not have been drawn, and
	// over-counting the roll is the safer error — it makes the operator load
	// media early rather than run out mid-session.
	return nil
}

func (q *Queue) failInterrupted(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx,
		`UPDATE print_jobs
		    SET state = 'failed',
		        error = 'the agent restarted while this job was printing; reprint if the sheet did not come out',
		        finished_at = ?
		  WHERE state = 'printing'`, q.now().Unix())
	return err
}

// LoadRoll records new media. 700 sheets is one roll.
func (q *Queue) LoadRoll(ctx context.Context, sheets int, note string) error {
	if sheets <= 0 {
		return errors.New("printer: a roll has a positive number of sheets")
	}
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO media_entries (id, kind, sheets, note, created_at) VALUES (?, 'load', ?, ?, ?)`,
		newID(), sheets, nullIfEmpty(note), q.now().Unix())
	if err != nil {
		return fmt.Errorf("printer: load roll: %w", err)
	}
	q.log.Info("printer: media loaded", "sheets", sheets, "note", note)
	return nil
}

// AdjustMedia writes a compensating entry — a recount after a jam, or media
// loaded before the counter existed. History is corrected by appending, never
// by editing, exactly as the loyalty ledger is.
func (q *Queue) AdjustMedia(ctx context.Context, sheets int, reason string) error {
	if sheets == 0 {
		return errors.New("printer: an adjustment of zero sheets changes nothing")
	}
	if reason == "" {
		return errors.New("printer: an adjustment needs a reason")
	}
	_, err := q.db.ExecContext(ctx,
		`INSERT INTO media_entries (id, kind, sheets, note, created_at) VALUES (?, 'adjust', ?, ?, ?)`,
		newID(), sheets, reason, q.now().Unix())
	if err != nil {
		return fmt.Errorf("printer: adjust media: %w", err)
	}
	return nil
}

// Remaining is SUM(sheets) over the ledger, never a stored total.
func (q *Queue) Remaining(ctx context.Context) (int, error) {
	var n sql.NullInt64
	if err := q.db.QueryRowContext(ctx, `SELECT SUM(sheets) FROM media_entries`).Scan(&n); err != nil {
		return 0, fmt.Errorf("printer: remaining: %w", err)
	}
	return int(n.Int64), nil
}

func (q *Queue) pendingSheets(ctx context.Context) (int, error) {
	var n sql.NullInt64
	if err := q.db.QueryRowContext(ctx,
		`SELECT SUM(sheets) FROM print_jobs WHERE state IN ('queued', 'printing')`,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("printer: pending: %w", err)
	}
	return int(n.Int64), nil
}

func (q *Queue) Get(ctx context.Context, id string) (Job, error) {
	return q.scanOne(ctx, `SELECT `+columns+` FROM print_jobs WHERE id = ?`, id)
}

// BySession returns a session's jobs, newest first.
func (q *Queue) BySession(ctx context.Context, sessionID string) ([]Job, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT `+columns+` FROM print_jobs WHERE session_id = ? ORDER BY queued_at DESC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("printer: by session: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

const columns = `id, session_id, layout, sheet_path, copies, sheets, state, error, queued_at, started_at, finished_at`

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Job, error) {
	var (
		j                     Job
		errText               sql.NullString
		startedAt, finishedAt sql.NullInt64
		queuedAt              int64
		layout, state         string
	)
	if err := row.Scan(&j.ID, &j.SessionID, &layout, &j.SheetPath, &j.Copies, &j.Sheets, &state,
		&errText, &queuedAt, &startedAt, &finishedAt); err != nil {
		return Job{}, err
	}
	j.Layout, j.State, j.Error = Layout(layout), State(state), errText.String
	j.QueuedAt = time.Unix(queuedAt, 0)
	if startedAt.Valid {
		j.StartedAt = time.Unix(startedAt.Int64, 0)
	}
	if finishedAt.Valid {
		j.FinishedAt = time.Unix(finishedAt.Int64, 0)
	}
	return j, nil
}

func (q *Queue) scanOne(ctx context.Context, query string, args ...any) (Job, error) {
	j, err := scan(q.db.QueryRowContext(ctx, query, args...))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Job{}, ErrNotFound
	case err != nil:
		return Job{}, fmt.Errorf("printer: query: %w", err)
	}
	return j, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("printer: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
