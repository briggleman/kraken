package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
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

// fileFailure marks err as a file operation's failure without rewriting it, for
// a runtime whose messages name no host path to begin with (the fake). It is
// what makes such a failure eligible for the filesystem classes at the gRPC
// boundary — see classifyError, which gives them to file operations only.
func fileFailure(op, p string, err error) error {
	if err == nil {
		return nil
	}
	return &fileOpError{op: op, path: p, causeMsg: hostCause(err).Error(), cause: err, msg: err.Error()}
}

// readErrs wraps a reader so that its READ failures, and only those, pass
// through wrap. io.Copy returns the reader's error and the writer's alike, and
// the two need different handling: a failed read of a node file carries the
// file's host path and must be rendered against the logical one, while a failed
// write is the gRPC stream going away and must reach the caller as it is.
type readErrs struct {
	r    io.Reader
	wrap func(error) error
}

func (e readErrs) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		err = e.wrap(err)
	}
	return n, err
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
//
// Only ABSOLUTE roots are scrubbed: a relative one ("backups") is a word, and
// would be rewritten wherever a message happened to contain it. Matching is
// separator-insensitive — a root configured as C:/kraken/backups must still
// catch the C:\kraken\backups\… that filepath.Join produced on the node — so
// the message's slashes are folded while a root is matched, and put back as
// they were everywhere else.
func (d *DockerRuntime) scrubHostPaths(serverID, s string) string {
	for _, root := range []string{d.dataDir, d.hostDataDir} {
		if serverID != "" {
			s = replaceRoot(s, filepath.Join(root, serverID), d.dataRoot())
		}
		s = replaceRoot(s, root, "<data dir>")
	}
	// A restore reads its archive from the backup store, which lives outside
	// the data dir: the node's default backup dir, or whatever dir the Panel
	// last configured (up to its first {{TOKEN}}, which is expanded per server).
	for _, root := range d.backupRoots() {
		s = replaceRoot(s, root, "<backup dir>")
	}
	return s
}

// replaceRoot replaces every occurrence of the absolute path root in s with
// repl, matching `/` and `\` as the same separator. A root that is not
// absolute, or is as short as a bare volume ("/", "C:\"), is never matched —
// either would rewrite far more than a path.
func replaceRoot(s, root, repl string) string {
	root = strings.TrimRight(root, `/\`)
	if len(root) <= 3 || !(filepath.IsAbs(root) || path.IsAbs(filepath.ToSlash(root))) {
		return s
	}
	fold := func(x string) string { return strings.ReplaceAll(x, `\`, "/") }
	froot := fold(root)
	var b strings.Builder
	for {
		i := strings.Index(fold(s), froot)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(repl)
		s = s[i+len(root):]
	}
}

// backupRoots lists the local backup directories a message could name.
func (d *DockerRuntime) backupRoots() []string {
	roots := []string{d.backupDir}
	d.bmu.RLock()
	if d.nodeCfg != nil {
		dir := d.nodeCfg.GetBackupDir()
		if i := strings.Index(dir, "{{"); i >= 0 {
			dir = dir[:i]
		}
		roots = append(roots, dir)
	}
	d.bmu.RUnlock()
	return roots
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
