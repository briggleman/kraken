package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// smbConfig holds the connection parameters for an SMB backup remote. The Agent
// speaks SMB2/3 as a client with these credentials (NTLMv2), so the destination
// needs no host-side setup — unlike the "share" target, which depends on an
// OS-level mount the service account can actually see (a mapped drive is
// invisible to LocalSystem, and a bare UNC path authenticates as the machine
// account).
type smbConfig struct {
	Host     string // "host[:port]" (port defaults to 445)
	Share    string // share name, e.g. "games"
	User     string
	Password string
	Domain   string // optional NTLM domain; empty is correct for most NAS devices
	BasePath string // directory inside the share for archives (share-relative)
}

// smbBackupTarget stores backup archives on an SMB server. It is used both as a
// primary backupTarget (backup_target == "smb") and as the mirror destination
// for replication. A fresh connection is dialed per operation — backups are
// infrequent, so connection reuse isn't worth the lifecycle cost.
type smbBackupTarget struct {
	cfg smbConfig
}

func (t *smbBackupTarget) Kind() string { return "smb" }

// smbDialTimeout bounds TCP connect plus the negotiate/session-setup handshake;
// transfers themselves are governed by the caller's context.
const smbDialTimeout = 15 * time.Second

// smbDefaultPort is the microsoft-ds port. NetBIOS-over-TCP (139) is not
// supported by the client library, so a host without a port always means 445.
const smbDefaultPort = "445"

// dial connects, authenticates and mounts the configured share. The caller must
// invoke the returned closeAll to release the tree connect, the session and the
// TCP connection.
func (t *smbBackupTarget) dial() (fs *smb2.Share, closeAll func() error, err error) {
	addr, err := smbHostPort(t.cfg.Host)
	if err != nil {
		return nil, nil, err
	}
	share := smbShareName(t.cfg.Share)
	if share == "" {
		return nil, nil, fmt.Errorf("smb: share name is required")
	}
	// The client library has no anonymous/null-session support, so a missing user
	// would otherwise fail deep inside session setup with an opaque error.
	if strings.TrimSpace(t.cfg.User) == "" {
		return nil, nil, fmt.Errorf("smb: a username is required")
	}

	conn, err := net.DialTimeout("tcp", addr, smbDialTimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("smb: dial %s: %w", addr, err)
	}
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{
		User:     t.cfg.User,
		Password: t.cfg.Password,
		Domain:   t.cfg.Domain,
	}}
	// The handshake context bounds negotiation and auth only — the session
	// deliberately does not inherit it, so the transfer isn't cut off mid-copy.
	hctx, cancel := context.WithTimeout(context.Background(), smbDialTimeout)
	defer cancel()
	sess, err := d.DialContext(hctx, conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("smb: authenticate to %s: %w", addr, err)
	}
	mounted, err := sess.Mount(share)
	if err != nil {
		_ = sess.Logoff()
		_ = conn.Close()
		return nil, nil, fmt.Errorf("smb: mount %q on %s: %w", share, addr, err)
	}
	return mounted, func() error {
		uerr := mounted.Umount()
		_ = sess.Logoff()
		_ = conn.Close()
		return uerr
	}, nil
}

// smbHostPort resolves the configured host to a dial address, defaulting the
// port to 445. Host may be a bare name/IP, an IPv6 literal, or either with an
// explicit port.
func smbHostPort(host string) (string, error) {
	h := strings.TrimSpace(host)
	if h == "" {
		return "", fmt.Errorf("smb: host is required")
	}
	if _, _, err := net.SplitHostPort(h); err == nil {
		return h, nil
	}
	// No (parseable) port: JoinHostPort re-brackets an IPv6 literal correctly.
	return net.JoinHostPort(strings.Trim(h, "[]"), smbDefaultPort), nil
}

// smbShareName reduces an operator-typed share to the bare share name the
// client mounts, so `\\nas\games`, `/games` and `games` all resolve to "games"
// (the server component is redundant — the connection already picked the host).
func smbShareName(share string) string {
	s := strings.ReplaceAll(strings.TrimSpace(share), `\`, "/")
	s = strings.Trim(s, "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// smbRelPath normalizes a configured path to the share-relative form the client
// requires: SMB paths are always relative to the mounted share, and the library
// rejects a leading separator outright — so an operator typing "/kraken/backups"
// (the natural thing to type) must not become a hard error.
func smbRelPath(p string) string {
	s := strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	s = strings.Trim(s, "/")
	if s == "" {
		return ""
	}
	s = path.Clean(s)
	if s == "." {
		return ""
	}
	return s
}

// remoteDir is the directory archives live in. The base path is always
// operator-configured, so it IS the destination — archives go directly in it (no
// per-server subdir), mirroring the local/share/sftp targets. Empty → the share
// root.
func (t *smbBackupTarget) remoteDir() string {
	return smbRelPath(t.cfg.BasePath)
}

// remotePath resolves an archive id to its share-relative path, guarding against
// traversal by using only the id's base name (mirrors localBackupTarget.path).
func (t *smbBackupTarget) remotePath(_ /*serverID*/, id string) string {
	return path.Join(t.remoteDir(), path.Base(id)+".tar.gz")
}

// Put uploads an archive: it skips when a complete copy already exists
// (idempotent re-runs / re-replication), writes to a ".part" file and renames it
// into place so a reader never sees a half-written archive (atomic publish). The
// .part is always rewritten from byte 0 — unlike SFTP there is no resume, since a
// spliced archive is worse than a repeated transfer.
func (t *smbBackupTarget) Put(ctx context.Context, serverID, id string, r io.Reader, size int64) error {
	share, closeAll, err := t.dial()
	if err != nil {
		return err
	}
	defer func() { _ = closeAll() }()
	fs := share.WithContext(ctx)

	if dir := t.remoteDir(); dir != "" {
		if err := fs.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("smb: mkdir %s: %w", dir, err)
		}
	}
	fp := t.remotePath(serverID, id)

	// Fast path: a complete copy is already there — nothing to transfer.
	if size > 0 {
		if fi, serr := fs.Stat(fp); serr == nil && fi.Size() == size {
			return nil
		}
	}

	part := fp + ".part"
	f, err := fs.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("smb: create %s: %w", part, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("smb: write %s: %w", part, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("smb: close %s: %w", part, err)
	}
	// Publish atomically: SMB rename never clobbers (ReplaceIfExists is off), so
	// drop any stale final first, then rename the completed .part into place.
	_ = fs.Remove(fp)
	if err := fs.Rename(part, fp); err != nil {
		return fmt.Errorf("smb: publish %s: %w", fp, err)
	}
	return nil
}

// smbReadCloser ties a remote file's lifetime to its SMB session so all three
// layers (file, session, connection) are released when the consumer closes the
// reader.
type smbReadCloser struct {
	f        io.ReadCloser
	closeAll func() error
}

func (r *smbReadCloser) Read(p []byte) (int, error) { return r.f.Read(p) }
func (r *smbReadCloser) Close() error {
	cerr := r.f.Close()
	_ = r.closeAll()
	return cerr
}

// Open returns a reader for an archive. The share is deliberately not bound to
// ctx: the reader outlives this call, and a caller that cancels after Open
// returns must not turn the consumer's reads into errors.
func (t *smbBackupTarget) Open(_ context.Context, serverID, id string) (io.ReadCloser, error) {
	share, closeAll, err := t.dial()
	if err != nil {
		return nil, err
	}
	f, err := share.Open(t.remotePath(serverID, id))
	if err != nil {
		_ = closeAll()
		return nil, fmt.Errorf("smb: open: %w", err)
	}
	return &smbReadCloser{f: f, closeAll: closeAll}, nil
}

func (t *smbBackupTarget) List(ctx context.Context, _ string) ([]*agentpb.BackupInfo, error) {
	share, closeAll, err := t.dial()
	if err != nil {
		return nil, err
	}
	defer func() { _ = closeAll() }()

	ents, err := share.WithContext(ctx).ReadDir(t.remoteDir())
	if err != nil {
		// A missing backup dir simply means no backups yet.
		if isSMBNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("smb: list: %w", err)
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

func (t *smbBackupTarget) Delete(ctx context.Context, serverID, id string) error {
	share, closeAll, err := t.dial()
	if err != nil {
		return err
	}
	defer func() { _ = closeAll() }()
	if err := share.WithContext(ctx).Remove(t.remotePath(serverID, id)); err != nil && !isSMBNotExist(err) {
		return fmt.Errorf("smb: delete: %w", err)
	}
	return nil
}

// verify dials the remote and ensures the base path exists (creating it when
// absent), so the Panel can surface reachability when config is applied.
func (t *smbBackupTarget) verify() error {
	share, closeAll, err := t.dial()
	if err != nil {
		return err
	}
	defer func() { _ = closeAll() }()
	base := t.remoteDir()
	if base == "" {
		// The share root always exists; mounting it was the reachability test.
		return nil
	}
	if err := share.MkdirAll(base, 0o750); err != nil {
		return fmt.Errorf("smb: ensure base path %q: %w", base, err)
	}
	return nil
}

// isSMBNotExist reports whether err is a "no such file" condition. The client
// maps STATUS_OBJECT_{NAME,PATH}_NOT_FOUND onto os.ErrNotExist inside an
// *os.PathError / *os.LinkError, so errors.Is unwraps to it.
func isSMBNotExist(err error) bool {
	return err != nil && errors.Is(err, os.ErrNotExist)
}
