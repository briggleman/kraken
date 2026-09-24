//go:build !windows

package api_test

import "syscall"

// inUseErrno is what this OS says when another process holds a file.
var inUseErrno error = syscall.EBUSY
