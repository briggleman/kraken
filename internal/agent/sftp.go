package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// sftpConfig holds the connection parameters for an SFTP backup remote. Exactly
// one of Password / PrivateKey authenticates; PrivateKey (PEM) wins when both
// are set.
type sftpConfig struct {
	Host       string // "host:port" (port defaults to 22)
	User       string
	Password   string
	PrivateKey string // PEM-encoded private key
	BasePath   string // remote root directory for archives
	// KnownHostKey pins the remote's SSH host key in authorized_keys format
	// ("<algo> <base64-key> [comment]"). Empty means trust-on-use (agent logs
	// a warning; production deployments should pin).
	KnownHostKey string
}

// sftpBackupTarget stores backup archives on a remote host over SFTP. It is used
// both as a primary backupTarget (backup_target == "sftp") and as the mirror
// destination for replication. A fresh SSH connection is dialed per operation —
// backups are infrequent, so connection reuse isn't worth the lifecycle cost.
type sftpBackupTarget struct {
	cfg sftpConfig
}

func (t *sftpBackupTarget) Kind() string { return "sftp" }

// dialTimeout bounds connection establishment; backup transfers themselves are
// governed by the caller's context.
const sftpDialTimeout = 15 * time.Second

// dial opens an SSH connection and an SFTP client. The caller must close both
// the returned client and the underlying ssh connection (via close).
func (t *sftpBackupTarget) dial() (sc *sftp.Client, close func() error, err error) {
	host := t.cfg.Host
	if host == "" {
		return nil, nil, fmt.Errorf("sftp: host is required")
	}
	if !strings.Contains(host, ":") {
		host += ":22"
	}

	var auth ssh.AuthMethod
	switch {
	case t.cfg.PrivateKey != "":
		signer, perr := ssh.ParsePrivateKey([]byte(t.cfg.PrivateKey))
		if perr != nil {
			return nil, nil, fmt.Errorf("sftp: parse private key: %w", perr)
		}
		auth = ssh.PublicKeys(signer)
	case t.cfg.Password != "":
		auth = ssh.Password(t.cfg.Password)
	default:
		return nil, nil, fmt.Errorf("sftp: a password or private key is required")
	}

	hostKeyCB, hkerr := hostKeyCallback(t.cfg.KnownHostKey, host)
	if hkerr != nil {
		return nil, nil, hkerr
	}
	cfg := &ssh.ClientConfig{
		User:            t.cfg.User,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: hostKeyCB,
		Timeout:         sftpDialTimeout,
	}
	conn, err := ssh.Dial("tcp", host, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("sftp: dial %s: %w", host, err)
	}
	client, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("sftp: open client: %w", err)
	}
	return client, func() error {
		cerr := client.Close()
		_ = conn.Close()
		return cerr
	}, nil
}

// hostKeyCallback returns the ssh.HostKeyCallback for the SFTP dial. When
// knownHostKey is set (authorized_keys format: "<algo> <base64-key> [comment]")
// the callback pins that key via ssh.FixedHostKey — any mismatch aborts the
// connection. When empty, falls back to ssh.InsecureIgnoreHostKey() and logs
// a one-time warning per dial so operators can grep for it in agent logs.
func hostKeyCallback(knownHostKey, host string) (ssh.HostKeyCallback, error) {
	trimmed := strings.TrimSpace(knownHostKey)
	if trimmed == "" {
		slog.Warn("sftp: no known host key pinned — accepting any key (MITM-vulnerable). Set sftp_known_host_key in node config to pin.", "host", host)
		return ssh.InsecureIgnoreHostKey(), nil // #nosec G106 -- explicit fallback when operator hasn't pinned; see warn log
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(trimmed))
	if err != nil {
		return nil, fmt.Errorf("sftp: parse known_host_key (expected authorized_keys format): %w", err)
	}
	return ssh.FixedHostKey(pub), nil
}

// remoteDir is the directory archives live in. The SFTP base path is always
// operator-configured, so it IS the destination — archives go directly in it
// (no per-server subdir), mirroring the local/share targets. Empty → the SSH
// home directory.
func (t *sftpBackupTarget) remoteDir() string {
	if t.cfg.BasePath == "" {
		return "."
	}
	return t.cfg.BasePath
}

// remotePath resolves an archive id to its remote path, guarding against
// traversal by using only the id's base name (mirrors localBackupTarget.path).
func (t *sftpBackupTarget) remotePath(_ /*serverID*/, id string) string {
	return path.Join(t.cfg.BasePath, path.Base(id)+".tar.gz")
}

// Put uploads an archive rsync-style: it skips when a complete copy already
// exists (idempotent re-runs / re-replication), uploads to a ".part" file and
// renames it into place so a reader never sees a half-written archive (atomic
// publish), and resumes from the partial's length when the source is seekable.
func (t *sftpBackupTarget) Put(_ context.Context, serverID, id string, r io.Reader, size int64) error {
	client, closeConn, err := t.dial()
	if err != nil {
		return err
	}
	defer func() { _ = closeConn() }()

	dir := t.remoteDir()
	if err := client.MkdirAll(dir); err != nil {
		return fmt.Errorf("sftp: mkdir %s: %w", dir, err)
	}
	fp := t.remotePath(serverID, id)

	// Fast path: a complete copy is already there — nothing to transfer.
	if size > 0 {
		if fi, serr := client.Stat(fp); serr == nil && fi.Size() == size {
			return nil
		}
	}

	// Resume from a prior partial when the source supports seeking and the .part
	// is shorter than the target; otherwise start fresh (truncate).
	part := fp + ".part"
	var offset int64
	if seeker, ok := r.(io.Seeker); ok && size > 0 {
		if fi, serr := client.Stat(part); serr == nil && fi.Size() > 0 && fi.Size() < size {
			if _, serr := seeker.Seek(fi.Size(), io.SeekStart); serr == nil {
				offset = fi.Size()
			}
		}
	}

	flags := os.O_WRONLY | os.O_CREATE
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	f, err := client.OpenFile(part, flags)
	if err != nil {
		return fmt.Errorf("sftp: create %s: %w", part, err)
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return fmt.Errorf("sftp: seek %s: %w", part, err)
		}
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("sftp: write %s: %w", part, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("sftp: close %s: %w", part, err)
	}
	// Publish atomically: drop any stale final (some servers' rename won't
	// clobber), then rename the completed .part into place.
	_ = client.Remove(fp)
	if err := client.Rename(part, fp); err != nil {
		return fmt.Errorf("sftp: publish %s: %w", fp, err)
	}
	return nil
}

// sftpReadCloser ties a remote file's lifetime to its SSH connection so both are
// released when the consumer closes the reader.
type sftpReadCloser struct {
	f         io.ReadCloser
	closeConn func() error
}

func (r *sftpReadCloser) Read(p []byte) (int, error) { return r.f.Read(p) }
func (r *sftpReadCloser) Close() error {
	cerr := r.f.Close()
	_ = r.closeConn()
	return cerr
}

func (t *sftpBackupTarget) Open(_ context.Context, serverID, id string) (io.ReadCloser, error) {
	client, closeConn, err := t.dial()
	if err != nil {
		return nil, err
	}
	f, err := client.Open(t.remotePath(serverID, id))
	if err != nil {
		_ = closeConn()
		return nil, fmt.Errorf("sftp: open: %w", err)
	}
	return &sftpReadCloser{f: f, closeConn: closeConn}, nil
}

func (t *sftpBackupTarget) List(_ context.Context, serverID string) ([]*agentpb.BackupInfo, error) {
	client, closeConn, err := t.dial()
	if err != nil {
		return nil, err
	}
	defer func() { _ = closeConn() }()

	ents, err := client.ReadDir(t.remoteDir())
	if err != nil {
		// A missing backup dir simply means no backups yet.
		if isSFTPNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sftp: list: %w", err)
	}
	var out []*agentpb.BackupInfo
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".tar.gz")
		name, created := parseBackupID(id, e.ModTime().UnixMilli())
		out = append(out, &agentpb.BackupInfo{Id: id, Name: name, Size: e.Size(), CreatedUnixMs: created})
	}
	sortBackups(out)
	return out, nil
}

func (t *sftpBackupTarget) Delete(_ context.Context, serverID, id string) error {
	client, closeConn, err := t.dial()
	if err != nil {
		return err
	}
	defer func() { _ = closeConn() }()
	if err := client.Remove(t.remotePath(serverID, id)); err != nil && !isSFTPNotExist(err) {
		return fmt.Errorf("sftp: delete: %w", err)
	}
	return nil
}

// verify dials the remote and ensures the base path exists (creating it when
// absent), so the Panel can surface reachability when config is applied.
func (t *sftpBackupTarget) verify() error {
	client, closeConn, err := t.dial()
	if err != nil {
		return err
	}
	defer func() { _ = closeConn() }()
	base := t.cfg.BasePath
	if base == "" {
		base = "."
	}
	if err := client.MkdirAll(base); err != nil {
		return fmt.Errorf("sftp: ensure base path %q: %w", base, err)
	}
	return nil
}

// isSFTPNotExist reports whether err is a "no such file" condition, whether it
// surfaces as an *sftp.StatusError or a wrapped os error.
func isSFTPNotExist(err error) bool {
	if err == nil {
		return false
	}
	var se *sftp.StatusError
	if errors.As(err, &se) {
		return se.FxCode() == sftp.ErrSSHFxNoSuchFile
	}
	return errors.Is(err, os.ErrNotExist)
}
