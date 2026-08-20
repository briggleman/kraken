//go:build !windows

package main

import (
	"fmt"
	"os"

	"github.com/briggleman/kraken/internal/agent/config"
)

// serviceMain rejects --service off Windows, where systemd (or compose) owns
// the agent's lifecycle instead; there is no SCM that could have launched us,
// so a plain start always falls through to the console path.
func serviceMain(_ *config.Config, action string) bool {
	if action != "" {
		fmt.Fprintln(os.Stderr, "agent: --service is only supported on Windows; on Linux use the systemd unit installed by deploy/install.sh")
		os.Exit(1)
	}
	return false
}
