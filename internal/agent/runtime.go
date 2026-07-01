// Package agent implements the node Agent: a gRPC server (one per host) that the
// Panel drives over mutual TLS to install, run, and observe game-server
// containers. The container backend is abstracted behind Runtime so the same
// gRPC surface can be served by a Docker implementation in production or an
// in-memory fake in tests and local development.
package agent

import (
	"context"
	"io"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// Runtime is the container backend the Agent drives. Streaming methods deliver
// items via an emit callback and run until the context is cancelled or the
// underlying source ends; returning a context error from emit stops the stream.
type Runtime interface {
	// NodeInfo reports the node's identity and capacity.
	NodeInfo(ctx context.Context) (*agentpb.NodeInfo, error)

	// Create records a server's runtime spec and provisions its data volume,
	// without starting it.
	Create(ctx context.Context, spec *agentpb.ServerSpec) error

	// Remove stops and removes a server's container, optionally deleting its data.
	Remove(ctx context.Context, serverID string, deleteData bool) error

	// ApplyConfig writes rendered config files (path → content) into the server's
	// data volume.
	ApplyConfig(ctx context.Context, serverID string, files map[string]string) error

	// ListFiles lists the entries directly under path in the server's volume.
	ListFiles(ctx context.Context, serverID, path string) ([]*agentpb.FileEntry, error)

	// ZipFiles writes a zip archive of the given volume paths to w.
	ZipFiles(ctx context.Context, serverID string, paths []string, w io.Writer) error

	// DownloadFile writes the raw (unzipped) bytes of a single file to w.
	DownloadFile(ctx context.Context, serverID, path string, w io.Writer) error

	// ReadFile returns the contents of a single file (capped at maxBytes), its
	// total size on disk, whether the content was truncated, and whether it looks
	// binary (contains NUL bytes).
	ReadFile(ctx context.Context, serverID, path string, maxBytes int64) (content []byte, size int64, truncated, binary bool, err error)

	// MakeDir creates a directory (and parents) in the volume.
	MakeDir(ctx context.Context, serverID, path string) error

	// MovePath renames/moves a file or directory within the volume.
	MovePath(ctx context.Context, serverID, src, dst string) error

	// CopyPath copies a file or directory within the volume.
	CopyPath(ctx context.Context, serverID, src, dst string) error

	// WriteFile writes/overwrites a single file in the volume.
	WriteFile(ctx context.Context, serverID, path string, content []byte) error

	// DeletePaths removes files/directories (recursively) from the volume.
	DeletePaths(ctx context.Context, serverID string, paths []string) error

	// CreateBackup snapshots the data volume into a node-local archive. slug is
	// the server's game-spec slug, used to expand path tokens (e.g. {{SLUG}}) in
	// the node's backup path; it is stable for the life of a server.
	CreateBackup(ctx context.Context, serverID, slug, name string) (*agentpb.BackupInfo, error)
	// ListBackups lists a server's backups.
	ListBackups(ctx context.Context, serverID, slug string) ([]*agentpb.BackupInfo, error)
	// RestoreBackup extracts a backup back into the data volume.
	RestoreBackup(ctx context.Context, serverID, slug, id string) error
	// DeleteBackup removes a backup archive.
	DeleteBackup(ctx context.Context, serverID, slug, id string) error

	// Install runs the one-shot install/update phase, emitting progress events.
	Install(ctx context.Context, req *agentpb.InstallServerRequest, emit func(*agentpb.InstallEvent) error) error

	// Power performs a power transition and returns the resulting state.
	Power(ctx context.Context, serverID string, action agentpb.PowerAction) (agentpb.ServerState, error)

	// Status returns a server's current state and last-known resources.
	Status(ctx context.Context, serverID string) (*agentpb.ServerStatus, error)

	// StreamConsole emits console lines (optionally replaying tail lines first).
	StreamConsole(ctx context.Context, serverID string, tail int32, emit func(*agentpb.ConsoleLine) error) error

	// SendCommand writes a command to a running server's console / stdin.
	SendCommand(ctx context.Context, serverID, command string) error

	// StreamStats emits periodic resource telemetry at roughly intervalMs.
	StreamStats(ctx context.Context, serverID string, intervalMs int32, emit func(*agentpb.ResourceStats) error) error

	// ApplyNodeConfig applies Panel-managed per-node config (backup target +
	// replication), hot-swapping the backup target. It returns whether the
	// configured target(s) are reachable and a human-readable status detail; it
	// does not error at the RPC level so the Panel can surface the detail.
	ApplyNodeConfig(ctx context.Context, cfg *agentpb.NodeConfig) (ok bool, detail string)

	// ReplicateBackups mirrors a server's existing archives from the primary
	// target to the configured SFTP remote, returning the counts copied/skipped.
	ReplicateBackups(ctx context.Context, serverID, slug string) (mirrored, skipped int32, err error)
}
