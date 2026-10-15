package agent

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// SteamCMD's depot cache, kept per server across install passes.
//
// An update is computed from two depot manifests: the build the tree holds and
// the build it is going to. SteamCMD keeps the first one in its own root —
// `depotcache/<depot>_<manifest>.manifest`, under C:\steamcmd on the steam-win
// image and $HOME/Steam on the Linux images — not in the force_install_dir,
// which only gets steamapps/appmanifest_<app>.acf. The install container used
// to throw that root away after every pass, so every real update had to ask the
// CDN for the old build's manifest again, and an anonymous login is refused a
// request code for a manifest that is no longer current on its branch.
// content_log.txt says so:
//
//	BYldRequestDepotManifest(App: 1963720, Depot: 1963721, Manifest: 1468257471765199732, branch: ): Failed to get manifest request code, 'Access Denied'
//	AppID 1963720 update canceled : Failed downloading 1 manifests (No connection)
//
// and the pass ends `state is 0x6 after update job`. validate does not help:
// it still wants the old manifest. That is the 0x6 that met almost every update
// (enshrouded-01 on 2026-10-15, dragonwilds-01 before it), reproduced on both
// OSes: with a fresh root the update fails, and with only the depotcache carried
// over it succeeds as a delta.
//
// So a pass that runs SteamCMD now gets a per-server cache dir bound where
// SteamCMD looks for it. It lives beside the data dirs, not in one, so backups,
// the Files tab and SFTP never see it, and it goes when the data does (Remove).
//
//   - Windows: bound straight onto C:\steamcmd\depotcache, whose parent the image
//     already has.
//   - Linux: bound at a neutral path and linked from $HOME/Steam/depotcache by
//     the script itself. Binding onto $HOME/Steam/depotcache directly makes the
//     daemon create $HOME/Steam owned by root, and SteamCMD — running as the
//     image's user — can then keep neither its app-info cache nor its logs:
//     both passes print "already up to date" over the old build. Verified on
//     kraken-steam-base, which is why the link is made as the image's user.
//
// A pass that ran without the cache (an older Agent, a cache that could not be
// made) still hits the 0x6 once; steamrecovery.go recovers that by moving the
// app manifest aside.

// steamCmdCacheDirName is the folder under the data root holding one depot
// cache per server. Server ids are UUIDs, so it never names a server.
const steamCmdCacheDirName = ".kraken-steamcmd"

// Where the cache is mounted in an install container.
const (
	steamCmdCacheWindowsTarget = `C:\steamcmd\depotcache`
	steamCmdCacheLinuxTarget   = "/kraken/steamcmd-depotcache"
)

// linkSteamCmdCache is the Linux script prefix that points SteamCMD's depot
// cache at the mounted dir. A real depotcache directory the image ships is left
// alone: replacing it is not this pass's call, and the pass still runs.
const linkSteamCmdCache = `if [ -n "$HOME" ] && mkdir -p "$HOME/Steam" 2>/dev/null && { [ -L "$HOME/Steam/depotcache" ] || [ ! -e "$HOME/Steam/depotcache" ]; } && ln -sfn ` +
	steamCmdCacheLinuxTarget + ` "$HOME/Steam/depotcache"; then :; else ` +
	`echo "[kraken] SteamCMD's depot cache is not kept on this pass: $HOME/Steam/depotcache could not be linked to ` + steamCmdCacheLinuxTarget + `" >&2; fi` + "\n"

// usesSteamCmd reports whether an install script runs SteamCMD.
func usesSteamCmd(script string) bool {
	return strings.Contains(strings.ToLower(script), "steamcmd")
}

// steamCmdCacheDir is the Agent's own path to a server's depot cache.
func (d *DockerRuntime) steamCmdCacheDir(serverID string) string {
	return filepath.Join(d.dataDir, steamCmdCacheDirName, serverID)
}

// steamCmdCacheSource is the daemon-visible path to the same dir (see
// bindSource).
func (d *DockerRuntime) steamCmdCacheSource(serverID string) string {
	return filepath.Join(d.hostDataDir, steamCmdCacheDirName, serverID)
}

// keepSteamCmdDepotCache mounts the server's depot cache into an install
// container whose script runs SteamCMD. It must run before shareDataWithGame
// on Linux, so the umask and the write probe still come first. A cache that
// cannot be made is said on the console and the pass runs without it.
func (d *DockerRuntime) keepSteamCmdDepotCache(serverID string, cfg *container.Config, host *container.HostConfig, note func(string)) {
	if len(cfg.Cmd) != 1 || !usesSteamCmd(cfg.Cmd[0]) {
		return
	}
	dir := d.steamCmdCacheDir(serverID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		note("SteamCMD's depot cache is not kept on this pass, so an update may end in state 0x6: " + err.Error())
		return
	}
	// The install container writes it as the image's user in the shared group.
	if gid, ok := d.sharedGroup(); ok {
		if err := shareDataTree(dir, os.Geteuid(), gid); err != nil {
			note("could not open SteamCMD's depot cache to the install container's group: " + err.Error())
		}
	}
	if d.isWindows() {
		host.Binds = append(host.Binds, d.steamCmdCacheSource(serverID)+":"+steamCmdCacheWindowsTarget)
		return
	}
	host.Binds = append(host.Binds, d.steamCmdCacheSource(serverID)+":"+steamCmdCacheLinuxTarget)
	cfg.Cmd = []string{linkSteamCmdCache + cfg.Cmd[0]}
}
