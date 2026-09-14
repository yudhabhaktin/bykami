//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// _CREATE_BREAKAWAY_FROM_JOB lets the helper escape any job object the
// service manager may have placed this process in, so the helper is not
// killed when the service stops.
const _CREATE_BREAKAWAY_FROM_JOB = 0x01000000

// OSDefaultSwapper is the platform-specific swapper for Windows. The key
// property is that Windows refuses to overwrite a running executable, but it
// does allow renaming it. So the outgoing binary is renamed to .previous,
// freeing the original path for the new binary.
//
// This half has been compiled for Windows but has never run on a real Windows
// service: the restart path that follows the swap is the untested half.
type OSDefaultSwapper struct {
	BinPath string
}

func (s *OSDefaultSwapper) Swap(verifiedNew string) error {
	prev := s.BinPath + ".previous"
	_ = os.Remove(prev)
	// Rename the running binary away. Windows allows this even while the
	// process is executing; the handle is on the file mapping, not the
	// directory entry.
	if err := os.Rename(s.BinPath, prev); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rename outgoing binary: %w", err)
	}
	if err := os.Rename(verifiedNew, s.BinPath); err != nil {
		return fmt.Errorf("move new binary into place: %w", err)
	}
	return nil
}

// Rollback renames the current (running) binary aside first, then moves the
// previous one into place. On Windows the rename of a running executable is
// allowed even though overwriting it is not.
func (s *OSDefaultSwapper) Rollback() error {
	current := s.BinPath + ".current"
	_ = os.Remove(current)
	if err := os.Rename(s.BinPath, current); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rename current binary aside: %w", err)
	}
	prev := s.BinPath + ".previous"
	if err := os.Rename(prev, s.BinPath); err != nil {
		return fmt.Errorf("restore previous binary: %w", err)
	}
	return nil
}

// Restart spawns a detached helper that stops the service, waits until it is
// no longer RUNNING, and starts it again.
//
// sc.exe has no "restart" verb, and calling sc stop from this process would
// kill this process before sc start could run. The helper is detached —
// CREATE_NEW_PROCESS_GROUP and CREATE_BREAKAWAY_FROM_JOB — so it outlives
// the service stop and completes the sequence.
//
// This has never run on Windows.
func (s *OSDefaultSwapper) Restart() error {
	sc := filepath.Join(os.Getenv("SystemRoot"), "System32", "sc.exe")
	bat := filepath.Join(os.TempDir(), "bykami-restart.bat")
	script := fmt.Sprintf(
		"@echo off\r\n"+
			"\"%s\" stop %s\r\n"+
			":wait\r\n"+
			"\"%s\" query %s | findstr RUNNING >nul && ping -n 2 127.0.0.1 >nul && goto wait\r\n"+
			"\"%s\" start %s\r\n"+
			"del \"%%~f0\"\r\n",
		sc, ServiceName, sc, ServiceName, sc, ServiceName)
	if err := os.WriteFile(bat, []byte(script), 0o644); err != nil {
		return fmt.Errorf("write restart helper: %w", err)
	}
	cmd := exec.Command("cmd", "/c", bat)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | _CREATE_BREAKAWAY_FROM_JOB,
	}
	return cmd.Start()
}
