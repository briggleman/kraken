//go:build !windows

package main

import (
	"log/slog"
	"os"
	"syscall"
)

// restartAgent hands the process over to the binary at exePath (which the
// self-updater has just installed or reverted). exec replaces the process
// image in place, so systemd sees an uninterrupted service — no unit
// configuration is involved at all. The state dir is the Windows build's
// business (its restart helper logs there); exec needs nothing of the sort.
func restartAgent(exePath, _ string) {
	if err := syscall.Exec(exePath, os.Args, os.Environ()); err != nil {
		slog.Error("selfupdate: exec new binary failed; exiting for the service manager to restart", "err", err, "exe", exePath)
		os.Exit(1)
	}
}
