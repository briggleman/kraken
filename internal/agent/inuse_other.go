//go:build !windows

package agent

import (
	"errors"
	"syscall"
)

// osInUse reports whether err is the kernel saying another process holds the
// file: EBUSY (a mount point, a busy device) or ETXTBSY (writing over a binary
// that is being executed — a game server's own executable while it runs).
func osInUse(err error) bool {
	return errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.ETXTBSY)
}
