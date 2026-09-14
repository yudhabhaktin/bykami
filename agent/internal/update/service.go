//go:build !windows

package update

import "errors"

// InstallService, UninstallService, and IsWindowsService are no-ops on
// non-Windows platforms.

var ErrNotWindows = errors.New("service management is only available on Windows")

func InstallService(_, _, _ string) error { return ErrNotWindows }
func UninstallService(_ string) error     { return ErrNotWindows }
func IsWindowsService() bool              { return false }
