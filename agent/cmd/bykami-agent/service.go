//go:build !windows

package main

import (
	"errors"
	"log/slog"
)

func runAsService(c config, log *slog.Logger) error {
	return errors.New("service mode is only available on Windows")
}
