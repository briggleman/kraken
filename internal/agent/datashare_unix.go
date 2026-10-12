//go:build !windows

package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// sharedDirBits and sharedFileBits are what shareDataTree adds: group read,
// write and search plus setgid on a dir, group read and write on a file. It
// only ever adds bits — nothing the tree already allows is taken away.
const (
	sharedDirBits  = fs.ModeSetgid | 0o070
	sharedFileBits = 0o060
)

// shareDataTree gives gid group access to root and to every entry under it
// that uid owns, and moves those entries into gid. Entries owned by anyone else
// are the game's: it created them under umask 0002 in a setgid dir, so they
// already are. Symlinks are left alone, and never followed.
//
// It walks the whole tree, so a large install pays one lstat per file; the
// common case changes nothing and writes nothing.
func shareDataTree(root string, uid, gid int) error {
	var errs []error
	walkErr := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			errs = append(errs, err)
			return nil
		}
		if e.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid {
			return nil
		}
		if int(st.Gid) != gid {
			if err := os.Lchown(path, -1, gid); err != nil {
				errs = append(errs, err)
				return nil
			}
		}
		bits := fs.FileMode(sharedFileBits)
		if e.IsDir() {
			bits = sharedDirBits
		}
		mode := info.Mode() & (fs.ModePerm | fs.ModeSetgid | fs.ModeSetuid | fs.ModeSticky)
		if mode&bits == bits && int(st.Gid) == gid {
			return nil
		}
		// A chown clears setgid on some filesystems, so the mode is set after it
		// from what the entry held before.
		if err := os.Chmod(path, mode|bits); err != nil {
			errs = append(errs, err)
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return fmt.Errorf("%d entries left unshared, first: %w", len(errs), errs[0])
	}
}
