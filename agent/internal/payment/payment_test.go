package payment_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/payment"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
	"github.com/bhaktiyudha/bykami/agent/internal/store"
)

var testPackage = session.Package{
	ID: "mini", Name: "MINI", PriceIDR: 45_000,
	TemplateID: "strip-3", PrintCopies: 1, TakeLimit: 15,
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func setup(t *testing.T, provider payment.Provider) (*payment.Store, *session.Store, *clock) {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	c := &clock{t: time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)}
	return payment.New(db, provider, payment.WithClock(c.now)),
		session.New(db, session.WithClock(c.now)), c
}

func startSession(t *testing.T, sessions *session.Store) session.Session {
	t.Helper()
	s, err := sessions.Start(context.Background(), "jajag", testPackage)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	return s
}

// The whole point of the package: money first, shutter second.
func TestSettlementReleasesTheShutter(t *testing.T) {
	sim := payment.NewSimulated(slog.New(slog.DiscardHandler), 0)
	payments, sessions, _ := setup(t, sim)
	ctx := context.Background()

	s := startSession(t, sessions)

	p, err := payments.Create(ctx, s.ID, testPackage.PriceIDR, payment.SessionKind)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	switch {
	case p.State != payment.Pending:
		t.Fatalf("state = %q, want pending", p.State)
	case p.QRPayload == "":
		t.Fatal("no QR payload for the customer to scan")
	case p.ExpiresAt.IsZero():
		t.Fatal("a QR code with no expiry holds the booth forever")
	}

	// Still pending, so the shutter is still locked.
	if _, err := payments.Refresh(ctx, p.ID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := sessions.MayFire(ctx); !errors.Is(err, session.ErrNotPaid) {
		t.Fatalf("the shutter opened before payment: %v", err)
	}

	// The customer pays.
	if err := sim.Settle(p.ExternalID); err != nil {
		t.Fatalf("settle: %v", err)
	}
	got, err := payments.Refresh(ctx, p.ID)
	if err != nil {
		t.Fatalf("refresh after payment: %v", err)
	}
	if got.State != payment.Settled {
		t.Fatalf("state = %q, want settled", got.State)
	}
	if got.SettledAt.IsZero() {
		t.Fatal("settled with no timestamp")
	}

	if _, err := sessions.MarkPaid(ctx, s.ID); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if _, err := sessions.MayFire(ctx); err != nil {
		t.Fatalf("the shutter is still locked after settlement: %v", err)
	}
}

// A provider that is slow or unreachable must not leave a customer staring at
// a dead QR code with no way forward, so expiry is decided locally too.
func TestExpiryIsDecidedLocally(t *testing.T) {
	sim := payment.NewSimulated(slog.New(slog.DiscardHandler), 0)
	payments, sessions, c := setup(t, sim)
	ctx := context.Background()

	s := startSession(t, sessions)
	p, err := payments.Create(ctx, s.ID, testPackage.PriceIDR, payment.SessionKind)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	c.t = c.t.Add(payment.DefaultTTL + time.Second)
	got, err := payments.Refresh(ctx, p.ID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got.State != payment.Expired {
		t.Fatalf("state = %q, want expired", got.State)
	}
}

// The kiosk polls, so the same answer arrives repeatedly by design.
func TestRefreshIsIdempotentAfterSettlement(t *testing.T) {
	sim := payment.NewSimulated(slog.New(slog.DiscardHandler), 0)
	payments, sessions, c := setup(t, sim)
	ctx := context.Background()

	s := startSession(t, sessions)
	p, err := payments.Create(ctx, s.ID, testPackage.PriceIDR, payment.SessionKind)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := sim.Settle(p.ExternalID); err != nil {
		t.Fatalf("settle: %v", err)
	}

	first, err := payments.Refresh(ctx, p.ID)
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	// A poll that arrives after the QR code would have expired must not undo a
	// payment that already settled.
	c.t = c.t.Add(payment.DefaultTTL * 2)
	second, err := payments.Refresh(ctx, p.ID)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.State != payment.Settled {
		t.Fatalf("a settled payment became %q", second.State)
	}
	if !first.SettledAt.Equal(second.SettledAt) {
		t.Fatal("a second poll moved the settlement time")
	}
}

func TestAutoSettleSettlesWithoutBeingTold(t *testing.T) {
	sim := payment.NewSimulated(slog.New(slog.DiscardHandler), time.Millisecond)
	payments, sessions, _ := setup(t, sim)
	ctx := context.Background()

	s := startSession(t, sessions)
	p, err := payments.Create(ctx, s.ID, testPackage.PriceIDR, payment.SessionKind)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	time.Sleep(5 * time.Millisecond)
	got, err := payments.Refresh(ctx, p.ID)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got.State != payment.Settled {
		t.Fatalf("state = %q, want settled", got.State)
	}
}

// No provider is the deployed default, and it must be a clear refusal rather
// than a nil dereference.
func TestNoProviderIsARefusal(t *testing.T) {
	payments, sessions, _ := setup(t, nil)
	ctx := context.Background()

	if payments.Enabled() {
		t.Fatal("payments reported enabled with no provider")
	}
	s := startSession(t, sessions)
	if _, err := payments.Create(ctx, s.ID, 45_000, payment.SessionKind); !errors.Is(err, payment.ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

func TestCreateRejectsANonPositiveAmount(t *testing.T) {
	sim := payment.NewSimulated(slog.New(slog.DiscardHandler), 0)
	payments, sessions, _ := setup(t, sim)
	ctx := context.Background()

	s := startSession(t, sessions)
	if _, err := payments.Create(ctx, s.ID, 0, payment.SessionKind); err == nil {
		t.Fatal("minted a QR code for nothing")
	}
}

// The simulated payload must not be a valid QRIS string. A banking app that
// scanned it and paid somebody would be a much worse bug than one that fails.
func TestSimulatedPayloadIsObviouslyFake(t *testing.T) {
	sim := payment.NewSimulated(slog.New(slog.DiscardHandler), 0)
	charge, err := sim.Create(context.Background(), "s1", 45_000)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !contains(charge.QRPayload, "SIMULATED") {
		t.Fatalf("payload %q does not announce itself as fake", charge.QRPayload)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

var _ = sql.ErrNoRows
