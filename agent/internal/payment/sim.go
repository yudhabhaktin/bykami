package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Simulated is the only provider that exists, and it takes no money.
//
// QRIS means Xendit, which is blocked on a business entity, NPWP and a bank
// account. That is days to weeks entirely outside the build, so the rest of the
// booth is built against this and the real provider drops into the same
// interface when the merchant account exists.
//
// It must never reach a booth that customers use: it would release the shutter
// for free. That is enforced rather than remembered — cmd/bykami-agent requires
// -payments=sim explicitly, warns at startup, and the flag's own help says so.
type Simulated struct {
	log *slog.Logger

	// autoSettle makes a charge settle by itself after a delay, so an
	// unattended rehearsal of the whole flow needs nobody to press anything.
	// Zero means it waits to be told.
	autoSettle time.Duration

	mu      sync.Mutex
	charges map[string]*simCharge
}

type simCharge struct {
	settleAt time.Time
	settled  bool
}

func NewSimulated(log *slog.Logger, autoSettle time.Duration) *Simulated {
	return &Simulated{log: log, autoSettle: autoSettle, charges: map[string]*simCharge{}}
}

func (s *Simulated) Name() string { return "simulated" }

func (s *Simulated) Create(_ context.Context, sessionID string, amountIDR int64) (Charge, error) {
	id := "sim_" + newExternalID()

	s.mu.Lock()
	c := &simCharge{}
	if s.autoSettle > 0 {
		c.settleAt = time.Now().Add(s.autoSettle)
	}
	s.charges[id] = c
	s.mu.Unlock()

	s.log.Warn("payment: simulated charge — no money is being taken",
		"session", sessionID, "amount_idr", amountIDR, "external_id", id)

	return Charge{
		ExternalID: id,
		// Shaped like a QRIS payload so the kiosk's renderer is exercised
		// honestly, but deliberately not valid: a real banking app scanning
		// this must fail, not pay somebody.
		QRPayload: fmt.Sprintf("00020101021226SIMULATED.BYKAMI.%s5802ID5405%d", id, amountIDR),
	}, nil
}

func (s *Simulated) Status(_ context.Context, externalID string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.charges[externalID]
	if !ok {
		return Failed, fmt.Errorf("simulated payment: no charge %q", externalID)
	}
	if c.settled {
		return Settled, nil
	}
	if !c.settleAt.IsZero() && !time.Now().Before(c.settleAt) {
		c.settled = true
		return Settled, nil
	}
	return Pending, nil
}

// Settle is the "the customer paid" button. The kiosk exposes it only while the
// simulated provider is selected.
func (s *Simulated) Settle(externalID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.charges[externalID]
	if !ok {
		return fmt.Errorf("simulated payment: no charge %q", externalID)
	}
	c.settled = true
	return nil
}

func newExternalID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("payment: entropy unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
