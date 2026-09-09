//go:build !windows

package agent

import "os"

// openShared is plain os.Open outside Windows — POSIX has no mandatory sharing
// modes to negotiate.
func openShared(fp string) (*os.File, error) { return os.Open(fp) }
