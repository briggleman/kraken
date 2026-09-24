//go:build windows

package agent

import (
	"errors"
	"syscall"
)

// The two Win32 errors a file held open by another process produces. Neither
// maps onto an fs sentinel (syscall.Errno.Is knows neither), so without these
// they would reach the Panel as an unclassified failure.
const (
	errSharingViolation syscall.Errno = 32 // ERROR_SHARING_VIOLATION
	errLockViolation    syscall.Errno = 33 // ERROR_LOCK_VIOLATION
)

// osInUse reports whether err is Windows saying another process holds the file:
// a game server that is still running has its saves, logs and binaries open,
// and a delete, move or overwrite of one fails this way.
func osInUse(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == errSharingViolation || errno == errLockViolation
}
