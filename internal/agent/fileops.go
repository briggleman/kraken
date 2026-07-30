package agent

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// localDir is the Agent's own path to the directory holding a server's data —
// what every native file op, backup walk, and the SFTP jail operate on. Use
// bindSource instead for anything handed to the Docker daemon.
func (d *DockerRuntime) localDir(serverID string) string {
	return filepath.Join(d.dataDir, serverID)
}

// bindSource is the *daemon-visible* path to a server's data directory: the
// source side of the bind mount in ContainerCreate. The daemon resolves bind
// sources against the host filesystem, not the Agent's mount namespace, so this
// is only the same string as localDir when the Agent shares the host's view of
// the data root (see hostDataDir in NewDockerRuntime).
func (d *DockerRuntime) bindSource(serverID string) string {
	return filepath.Join(d.hostDataDir, serverID)
}

// localOf maps an in-container absolute path (under the data root, as returned
// by safePath) to the Agent's own filesystem path.
func (d *DockerRuntime) localOf(serverID, containerAbs string) string {
	rel := strings.TrimPrefix(containerAbs, d.dataRoot())
	rel = strings.TrimPrefix(rel, "/")
	return filepath.Join(d.localDir(serverID), filepath.FromSlash(rel))
}

// containerDataTarget is the in-container mount point for a server's data dir
// (backslash form on Windows so it resolves correctly).
func (d *DockerRuntime) containerDataTarget(serverID string) string {
	if spec, ok := d.getSpec(serverID); ok && spec.DataPath != "" {
		return d.containerPath(spec.DataPath)
	}
	return d.containerPath(d.dataRoot())
}

// copyTreeFS recursively copies src → dst on the host filesystem (file or dir).
func copyTreeFS(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFileFS(src, dst, info)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTreeFS(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFileFS(src, dst string, info os.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// withinHostDir reports whether p is inside the server's host data dir (a
// defense-in-depth guard against archive/zip path traversal on restore).
func (d *DockerRuntime) withinHostDir(serverID, p string) bool {
	root := filepath.Clean(d.localDir(serverID))
	c := filepath.Clean(p)
	return c == root || strings.HasPrefix(c, root+string(os.PathSeparator))
}

// containerJoin joins an in-container directory path with a child name using the
// data-root namespace (forward slashes), for FileEntry.path values.
func containerJoin(dir, name string) string { return path.Join(dir, name) }
