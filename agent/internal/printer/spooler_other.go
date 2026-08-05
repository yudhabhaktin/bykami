//go:build !windows

package printer

import (
	"context"
	"errors"
)

// errNotWindows is what a developer's laptop gets, and it is returned from
// NewSpooler rather than from the first print.
//
// A booth that accepted the flag and then failed in front of a paying customer
// would have taken money for a print it never had any way to make.
// design/kiosk.md records why the booth PC is Windows 11 Pro, and the Linux
// alternative if that ever has to be revisited.
var errNotWindows = errors.New("printer: printing through the Windows spooler needs Windows; use -printer=sim off the booth PC")

func spoolerSupported() error { return errNotWindows }

func probeQueue(string) error { return errNotWindows }

func (s *Spooler) spool(context.Context, spoolJob) error { return errNotWindows }
