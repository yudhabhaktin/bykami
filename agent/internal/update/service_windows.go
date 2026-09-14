//go:build windows

package update

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// ServiceName is the name the booth service is registered under.
const ServiceName = "bykami-agent"

// InstallService registers the agent as a Windows service.
//
// This has never run on Windows.
func InstallService(binPath, displayName, desc string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(ServiceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", ServiceName)
	}

	abs, err := filepath.Abs(binPath)
	if err != nil {
		return err
	}

	cfg := mgr.Config{
		DisplayName: displayName,
		Description: desc,
		StartType:   mgr.StartAutomatic,
	}
	s, err = m.CreateService(ServiceName, abs, cfg)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	// Restart on failure.
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * 1000}, // 5 seconds
		{Type: mgr.ServiceRestart, Delay: 10 * 1000},
		{Type: mgr.ServiceRestart, Delay: 30 * 1000},
	}, 60)

	return nil
}

// UninstallService removes the Windows service.
//
// This has never run on Windows.
func UninstallService(name string) error {
	if name == "" {
		name = ServiceName
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	status, err := s.Control(svc.Stop)
	if err != nil {
		// Service may already be stopped.
		_ = status
	}
	return s.Delete()
}

// IsWindowsService reports whether the process is running as a Windows service.
func IsWindowsService() bool {
	is, _ := svc.IsWindowsService()
	return is
}
