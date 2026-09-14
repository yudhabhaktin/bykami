//go:build !windows

package update

import (
	"fmt"
	"os"
	"os/exec"
)

// OSDefaultSwapper is the platform-specific swapper used when none is
// provided. On non-Windows platforms it atomically replaces the binary and
// runs systemctl restart.
type OSDefaultSwapper struct {
	BinPath string
}

func (s *OSDefaultSwapper) Swap(verifiedNew string) error {
	prev := s.BinPath + ".previous"
	_ = os.Remove(prev)
	if err := os.Rename(s.BinPath, prev); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rename outgoing binary: %w", err)
	}
	if err := os.Rename(verifiedNew, s.BinPath); err != nil {
		return fmt.Errorf("move new binary into place: %w", err)
	}
	if err := os.Chmod(s.BinPath, 0o755); err != nil {
		return fmt.Errorf("chmod new binary: %w", err)
	}
	return nil
}

func (s *OSDefaultSwapper) Rollback() error {
	prev := s.BinPath + ".previous"
	if _, err := os.Stat(prev); err != nil {
		return fmt.Errorf("no previous binary to roll back to: %w", err)
	}
	if err := os.Rename(prev, s.BinPath); err != nil {
		return fmt.Errorf("restore previous binary: %w", err)
	}
	return nil
}

func (s *OSDefaultSwapper) Restart() error {
	return exec.Command("systemctl", "restart", "bykami-agent").Run()
}
