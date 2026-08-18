//go:build windows

package main

import (
	"log/slog"
	"os"
)

// restartAgent exits so the process comes back up on the binary now at
// exePath. Windows has no exec(2): under the SCM the non-zero exit trips the
// service's restart-on-failure recovery actions (installed by
// --service install), which relaunch the service — onto the swapped binary —
// within seconds. From an interactive console the operator restarts by hand.
func restartAgent(exePath string) {
	if isWindowsService() {
		slog.Info("selfupdate: exiting for SCM recovery to restart the service", "exe", exePath)
	} else {
		slog.Info("selfupdate: binary swapped — restart the agent to run it", "exe", exePath)
	}
	os.Exit(1)
}
