package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// backupTarget abstracts where a server's backup archives live. The Agent uses
// a node-local filesystem target by default, or an SFTP remote when configured.
// Archives are gzipped tarballs keyed by an opaque id that encodes the creation
// time and name.
type backupTarget interface {
	Put(ctx context.Context, serverID, id string, r io.Reader, size int64) error
	Open(ctx context.Context, serverID, id string) (io.ReadCloser, error)
	List(ctx context.Context, serverID string) ([]*agentpb.BackupInfo, error)
	Delete(ctx context.Context, serverID, id string) error
	Kind() string
}

// expandBackupPath substitutes dynamic-naming tokens in a node's backup path
// (backup_dir or sftp_base_path) for a specific server. Today the only token is
// {{SLUG}}, the server's game-spec slug — stable for the life of a server, so
// the resolved path is identical across create/list/restore/delete and backups
// don't go missing. A path with no tokens is returned unchanged. The slug is
// sanitized to a safe path segment so a hostile spec can't escape the root.
func expandBackupPath(p, slug string) string {
	if p == "" {
		return p
	}
	return strings.NewReplacer(
		"{{SLUG}}", sanitizePathToken(slug),
	).Replace(p)
}

// sanitizePathToken reduces a token value to a single safe path segment: any
// character outside [A-Za-z0-9._-] becomes '-', so values can't contain path
// separators or "..". An empty result falls back to "unknown" so the path never
// collapses into a shared parent (which would mix two servers' backups).
func sanitizePathToken(v string) string {
	out := make([]rune, 0, len(v))
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	s := strings.Trim(string(out), "-.")
	if s == "" {
		return "unknown"
	}
	return s
}

// staticPrefix returns the leading directory of a (possibly templated) path that
// contains no tokens — e.g. "/media/games/{{SLUG}}/backup" → "/media/games".
// Used to verify a templated share path: the per-server subdir doesn't exist
// until a backup runs, but the mount root does, so checking the prefix still
// catches an unmounted share. A path with no tokens is returned unchanged.
func staticPrefix(p string) string {
	i := strings.Index(p, "{{")
	if i < 0 {
		return p
	}
	p = p[:i]
	if j := strings.LastIndexAny(p, `/\`); j >= 0 {
		p = p[:j]
	}
	if p == "" {
		p = string(filepath.Separator)
	}
	return p
}

// parseBackupID splits an archive id ("<unixMs>__<name>") into its display name
// and creation timestamp; missing parts fall back to sensible defaults.
func parseBackupID(id string, modUnixMs int64) (name string, created int64) {
	name, created = id, modUnixMs
	if i := strings.Index(id, "__"); i >= 0 {
		name = id[i+2:]
		if ms, err := strconv.ParseInt(id[:i], 10, 64); err == nil {
			created = ms
		}
	}
	return name, created
}

// ---- node-local filesystem target ----

type localBackupTarget struct {
	dir string // root backup directory
	// flat stores archives directly in dir rather than under a per-server subdir.
	// Set for operator-configured paths (incl. templated ones like
	// /media/games/{{SLUG}}/backup) — the configured path IS the destination. The
	// zero-config default (empty backup_dir) keeps flat=false so multiple servers
	// on a node don't silently share one directory.
	flat bool
}

func (t *localBackupTarget) Kind() string { return "local" }

func (t *localBackupTarget) serverDir(serverID string) string {
	if t.flat {
		return t.dir
	}
	return filepath.Join(t.dir, serverID)
}

// path resolves an id to its archive path, guarding against traversal.
func (t *localBackupTarget) path(serverID, id string) (string, error) {
	dir := t.serverDir(serverID)
	fp := filepath.Join(dir, filepath.Base(id)+".tar.gz")
	if filepath.Dir(fp) != filepath.Clean(dir) {
		return "", fmt.Errorf("backup: invalid id")
	}
	return fp, nil
}

func (t *localBackupTarget) Put(_ context.Context, serverID, id string, r io.Reader, _ int64) error {
	dir := t.serverDir(serverID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("backup: dir: %w", err)
	}
	fp, err := t.path(serverID, id)
	if err != nil {
		return err
	}
	f, err := os.Create(fp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		_ = os.Remove(fp)
		return fmt.Errorf("backup: write: %w", err)
	}
	return f.Close()
}

func (t *localBackupTarget) Open(_ context.Context, serverID, id string) (io.ReadCloser, error) {
	fp, err := t.path(serverID, id)
	if err != nil {
		return nil, err
	}
	return os.Open(fp)
}

func (t *localBackupTarget) List(_ context.Context, serverID string) ([]*agentpb.BackupInfo, error) {
	ents, err := os.ReadDir(t.serverDir(serverID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*agentpb.BackupInfo
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".tar.gz")
		info, _ := e.Info()
		var size, mod int64
		if info != nil {
			size = info.Size()
			mod = info.ModTime().UnixMilli()
		}
		name, created := parseBackupID(id, mod)
		out = append(out, &agentpb.BackupInfo{Id: id, Name: name, Size: size, CreatedUnixMs: created})
	}
	sortBackups(out)
	return out, nil
}

func (t *localBackupTarget) Delete(_ context.Context, serverID, id string) error {
	fp, err := t.path(serverID, id)
	if err != nil {
		return err
	}
	if err := os.Remove(fp); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ---- network-share target (a mounted SMB/NFS path) ----

// shareBackupTarget stores archives on a network share the operator has mounted
// onto the host (SMB on Windows, SMB/NFS on Linux), pointed at by its dir. It is
// behaviourally identical to localBackupTarget — native file ops, so it works
// the same on Linux and Windows without any rsync/SFTP protocol — but it reports
// kind "share" and verify() requires the mount to already exist and be writable
// (it is never auto-created, so a missing/unmounted share surfaces as an error).
type shareBackupTarget struct {
	localBackupTarget
}

func (t *shareBackupTarget) Kind() string { return "share" }

// verify confirms the share path is a mounted, writable directory. The Panel
// calls this on ApplyNodeConfig so a bad mount fails loudly at save time.
func (t *shareBackupTarget) verify() error {
	if strings.TrimSpace(t.dir) == "" {
		return fmt.Errorf("share path is required")
	}
	// A templated path (e.g. /mnt/nas/{{SLUG}}/backup) has no per-server subdir
	// until a backup runs, so verify the static mount prefix instead — that's
	// what catches an unmounted share. A non-templated path verifies as-is.
	check := staticPrefix(t.dir)
	fi, err := os.Stat(check)
	if err != nil {
		return fmt.Errorf("share path %q not accessible — is the network share mounted?: %w", check, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("share path %q is not a directory", check)
	}
	probe := filepath.Join(check, ".kraken-write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0o640); err != nil {
		return fmt.Errorf("share path %q is not writable: %w", check, err)
	}
	_ = os.Remove(probe)
	return nil
}

func sortBackups(b []*agentpb.BackupInfo) {
	sort.Slice(b, func(i, j int) bool { return b[i].CreatedUnixMs > b[j].CreatedUnixMs })
}
