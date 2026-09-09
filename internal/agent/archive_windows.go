//go:build windows

package agent

import (
	"os"

	"golang.org/x/sys/windows"
)

// openShared opens fp read-only with FILE_SHARE_DELETE on top of the
// read/write sharing os.Open grants. Without it, the archiver holding a save
// file open makes the game's atomic-save rename (ReplaceFile/MoveFileEx over
// the original) fail *inside the game* for the duration of the backup — the
// backup must never hurt the running server.
func openShared(fp string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(fp)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: fp, Err: err}
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: fp, Err: err}
	}
	return os.NewFile(uintptr(h), fp), nil
}
