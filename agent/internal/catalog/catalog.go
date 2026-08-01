// Package catalog is what the customer can buy at the booth.
//
// # This duplicates packages/content, and that is a known cost
//
// The marketing sites' catalogue lives in packages/content/src/verticals/studio.ts
// and is the source of truth for prices. It is TypeScript, and the agent is a Go
// binary cross-compiled for a Windows PC that may be offline, so it cannot read
// it. The options were a build-time generator, a runtime fetch from the API, or
// this — and the first two both make the booth's price list depend on something
// that can be unavailable at the moment a customer is standing there.
//
// The cost is drift: a price changed on the website and not here is a booth
// charging yesterday's price. catalog_test.go reads studio.ts and fails when
// the two disagree, which turns drift from a silent bug into a red build.
//
// # The prices are marked unverified upstream
//
// studio.ts records every price as read off a gitignored PDF and never
// confirmed with the owner. They are copied here unchanged rather than
// re-guessed, so there is exactly one thing to correct when they are confirmed.
//
// # The booth sells one thing
//
// studio.ts still lists four packages, because the studio still sells four at
// the counter. The self-service booth sells one — there is no attendant to
// explain the difference between MIDI and MAXI to somebody standing at a screen,
// and a price list is a step between a customer and the camera that earns
// nothing. The one entry keeps the id `mini`, which is what anchors its price to
// studio.ts and keeps the drift guard honest; its name is what the screen says.
package catalog

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

//go:embed packages.json
var packagesJSON []byte

// ErrNotFound means the kiosk asked for a package that is not on the list.
var ErrNotFound = errors.New("catalog: no such package")

// ReprintIDR is what one extra print costs once the session's included print
// has been claimed.
//
// A price, and therefore unverified in exactly the way every price in studio.ts
// is: read off nothing, agreed with nobody, and here so that there is one place
// to correct it. It is deliberately not in packages.json — a reprint is not a
// session, and giving it a package id would put something on the price list
// that cannot be bought at the start of one.
const ReprintIDR = 20_000

// Package is one purchasable session.
type Package struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PriceIDR int64  `json:"price_idr"`

	// DurationMinutes is how long the customer has in front of the camera, and
	// the kiosk now counts it down on the capture screen.
	//
	// Still not enforced here. The agent refusing a frame because a timer
	// expired would cut a customer off mid-pose with money already taken; the
	// take limit is the hard bound on what a session can cost, and this is the
	// pacing that stops one group holding the booth while a queue forms. The
	// screen reads it from here so the number counted down is the number sold.
	DurationMinutes int `json:"duration_minutes"`

	// TakeLimit is enforced, at capture, because the app owns the shutter.
	TakeLimit int `json:"take_limit"`

	// TemplateID is the default design. The customer can pick another with the
	// same layout; this is what the screen opens on.
	TemplateID string `json:"template_id"`

	// PrintCopies is what the package includes. The selection step enforces it,
	// which is the backstop for a stray file never becoming a free print.
	PrintCopies int `json:"print_copies"`
}

// All returns the catalogue in the order the screen should show it.
func All() ([]Package, error) {
	var out []Package
	if err := json.Unmarshal(packagesJSON, &out); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	for _, p := range out {
		if err := p.validate(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Only returns the single package this booth sells.
//
// The kiosk no longer names one. It has nothing to choose from, so asking it to
// send an id back would be a round trip whose only possible answer is already
// known here — and one more way for the screen and the charge to disagree.
func Only() (Package, error) {
	all, err := All()
	if err != nil {
		return Package{}, err
	}
	if len(all) != 1 {
		// The booth's whole flow assumes it: there is no price list to show and
		// no screen on which a second package could be picked, so a catalogue
		// with two of them would silently sell whichever came first.
		return Package{}, fmt.Errorf("catalog: the booth sells one package, the catalogue has %d", len(all))
	}
	return all[0], nil
}

// Get returns one package by id.
func Get(id string) (Package, error) {
	all, err := All()
	if err != nil {
		return Package{}, err
	}
	for _, p := range all {
		if p.ID == id {
			return p, nil
		}
	}
	return Package{}, fmt.Errorf("%w: %q", ErrNotFound, id)
}

func (p Package) validate() error {
	switch {
	case p.ID == "" || p.Name == "":
		return fmt.Errorf("catalog: package %q is missing an id or name", p.ID)
	case p.PriceIDR <= 0:
		// A free package would open the shutter with no payment, which is the
		// one thing the booth's payment gate exists to prevent.
		return fmt.Errorf("catalog: package %q has no price", p.ID)
	case p.TakeLimit <= 0:
		return fmt.Errorf("catalog: package %q has no take limit", p.ID)
	case p.PrintCopies <= 0:
		return fmt.Errorf("catalog: package %q includes no prints", p.ID)
	case p.TemplateID == "":
		return fmt.Errorf("catalog: package %q has no default template", p.ID)
	}
	return nil
}
