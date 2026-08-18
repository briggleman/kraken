//go:build !windows

package main

import (
	"fmt"

	"github.com/briggleman/kraken/internal/agent/config"
)

// isWindowsService is always false off Windows.
func isWindowsService() bool { return false }

// runService only exists to satisfy main's service branch; unreachable when
// isWindowsService is constant-false, but kept honest anyway.
func runService(*config.Config) error {
	return fmt.Errorf("agent: service mode is only supported on Windows")
}

// serviceControl rejects --service on non-Windows hosts, where systemd (or
// compose) owns the agent's lifecycle instead.
func serviceControl(string, *config.Config) error {
	return fmt.Errorf("agent: --service is only supported on Windows; on Linux use the systemd unit installed by deploy/install.sh")
}
