package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// fileOpError is a file operation's failure in the form an API client may see.
//
// Its message names the LOGICAL path (/data/…) and never the node's host path:
// whatever a file op returns reaches the browser verbatim, and the
// *fs.PathError / *os.LinkError the os package hands back carries the resolved
// host path — so passing one through would teach anyone with server.files
// access where the node keeps its storage. Its Unwrap still yields the OS cause,
// though, because the gRPC boundary (classifyError) needs errors.Is on it to
// tell a missing file from a locked one from a refused one — the distinction
// the operator acts on.
type fileOpError struct {
	op       string // "delete", "move", … — "" for the stat family, whose text predates this type
	path     string // the logical path the operation was about
	causeMsg string // the cause's text, already scrubbed of host paths
	cause    error  // the OS cause, for errors.Is/As
	msg      string // the whole message, already scrubbed
}

func (e *fileOpError) Error() string { return e.msg }
func (e *fileOpError) Unwrap() error { return e.cause }

// badPathError is a refusal on the path alone: the message it has always had,
// matched by errors.Is(err, ErrBadPath) so the Panel answers it with a 400.
type badPathError struct{ msg string }

func (e *badPathError) Error() string        { return e.msg }
func (e *badPathError) Is(target error) bool { return target == ErrBadPath }

func badPath(format string, args ...any) error {
	return &badPathError{msg: fmt.Sprintf(format, args...)}
}

// hostCause strips the wrappers that carry a filesystem path (*fs.PathError,
// *os.LinkError) down to the OS error inside them — the errno, which says what
// went wrong and names no path. Anything else is returned as it is.
func hostCause(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Err != nil {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) && le.Err != nil {
		return le.Err
	}
	return err
}

// fileErr renders a failed file operation against the logical path p. detail is
// appended to the subject in the message only (a move's " → dst"). The OS cause
// is stripped of its path wrapper and, belt and braces, any host data-dir
// prefix still in its text is rewritten to the logical root — an error from a
// helper that formatted a host path into a plain string must not leak either.
func (d *DockerRuntime) fileErr(serverID, op, p, detail string, err error) error {
	cause := hostCause(err)
	causeMsg := d.scrubHostPaths(serverID, cause.Error())
	return &fileOpError{
		op:       op,
		path:     p,
		causeMsg: causeMsg,
		cause:    cause,
		msg:      fmt.Sprintf("docker: %s %s%s: %s", op, p, detail, causeMsg),
	}
}

// scrubbed keeps err's whole chain (so it classifies exactly as before) but
// replaces its text with one that names no host path. It is for failures whose
// own message carries context worth keeping — a restore that "stopped at" a
// named unit — where stripping to the bare errno would lose it.
func (d *DockerRuntime) scrubbed(serverID string, err error) error {
	if err == nil {
		return nil
	}
	msg := d.scrubHostPaths(serverID, err.Error())
	return &fileOpError{causeMsg: msg, cause: err, msg: msg}
}

// scrubHostPaths rewrites the node's host storage paths in s to the logical
// data root. The server's own dir maps to the root the file browser shows; any
// other path under the data dir (another server's, a staging dir) is named only
// as "<data dir>".
func (d *DockerRuntime) scrubHostPaths(serverID, s string) string {
	for _, root := range []string{d.dataDir, d.hostDataDir} {
		// A root this short ("/", "C:\") would rewrite every separator in the
		// message; no real data dir is one, so it is skipped rather than trusted.
		if len(root) <= 3 {
			continue
		}
		if serverID != "" {
			s = strings.ReplaceAll(s, filepath.Join(root, serverID), d.dataRoot())
		}
		s = strings.ReplaceAll(s, root, "<data dir>")
	}
	return s
}

// statError renders a filesystem failure (stat, open, read) against the
// logical path, never the host one. Anything unrecognized is reported by its
// underlying cause (the syscall errno, which an *os.PathError wraps) rather
// than the PathError itself, whose Error() would print the host path we are
// keeping out of the response. The cause stays reachable through Unwrap, so a
// missing file still reaches the Panel as a 404 and a locked one as a 409.
func statError(p string, err error) error {
	cause := hostCause(err)
	fo := &fileOpError{path: p, causeMsg: cause.Error(), cause: cause}
	switch {
	case os.IsNotExist(err):
		fo.msg = fmt.Sprintf("docker: %s not found", p)
	case os.IsPermission(err):
		fo.msg = fmt.Sprintf("docker: %s: permission denied", p)
	default:
		fo.msg = fmt.Sprintf("docker: %s: %v", p, cause)
	}
	return fo
}
