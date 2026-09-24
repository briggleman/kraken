//go:build windows

package api_test

import "syscall"

// inUseErrno is what this OS says when another process holds a file:
// ERROR_SHARING_VIOLATION, the error behind #352.
var inUseErrno error = syscall.Errno(32)
