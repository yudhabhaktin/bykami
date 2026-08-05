//go:build !windows

package printer

import (
	"errors"
	"testing"
)

// The booth PC is Windows, and a booth that accepted this flag anywhere else
// would fail at the first print — which is after the customer has paid.
func TestNewSpoolerRefusesOffWindows(t *testing.T) {
	_, err := NewSpooler(SpoolerConfig{Queue: "DS-RX1", CutQueue: "DS-RX1 cut"}, quietLog())
	if !errors.Is(err, errNotWindows) {
		t.Fatalf("got %v, want the backend to refuse to start off Windows", err)
	}
}
