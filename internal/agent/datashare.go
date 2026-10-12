package agent

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// A server's data dir has two writers on a Linux node: the Agent (rendered
// config, restores, Files-tab edits, SFTP uploads, deleting the tree on retire)
// and the game container (SteamCMD's install, the game's own saves and config
// rewrites). They are rarely the same user. install.sh runs the Agent as the
// `kraken` system user (uid 995 on abyss-lnx), a compose Agent runs as root, and
// the steam images run as `steam`, uid 1000 — which owns the image's baked Wine
// prefix, and Wine refuses a prefix it does not own, so the game cannot simply
// run as the Agent's uid instead.
//
// Before this, the dir was the Agent's alone (0755), the game could not write
// it, and SteamCMD said "Success! App … fully installed" while writing nothing
// to it: the first start exited 53, Wine finding no server exe (#425).
//
// The fix is a group both sides are in, rather than a uid they share:
//
//   - the dir is group-writable and setgid, so everything created under it, by
//     either side, carries the Agent's group (shareDataDir);
//   - the game container keeps its image user but gets the Agent's gid as a
//     supplementary group, and runs under umask 0002 so what it creates stays
//     group-writable (shareDataWithGame).
//
// Windows nodes need none of it: the Windows daemon runs containers as
// ContainerAdministrator over a bind mount the Agent's account can write.

// exitDataDirNotWritable is the install container's exit code when its write
// probe fails. 73 is sysexits' EX_CANTCREAT.
const exitDataDirNotWritable = 73

// writeProbeName is the file the install probe creates and removes at the top
// of the data dir.
const writeProbeName = ".kraken-write-probe"

// agentGroupID is the group the data dir is shared through: the Agent's own
// effective gid. False where there is no POSIX group to share (an Agent on a
// Windows host, Docker Desktop included). A variable so tests can pin it.
var agentGroupID = func() (int, bool) {
	if runtime.GOOS == "windows" {
		return 0, false
	}
	gid := os.Getegid()
	return gid, gid >= 0
}

// sharedGroup is the gid this node's game containers share the data dir
// through, when they need one.
func (d *DockerRuntime) sharedGroup() (int, bool) {
	if d.isWindows() {
		return 0, false
	}
	return agentGroupID()
}

// shareDataWithGame adds the shared group and the umask to a Linux container
// that bind-mounts a server's data dir. An install container also gets the
// write probe, so a dir the game cannot write fails the pass at once instead
// of letting SteamCMD report success over nothing (#425).
//
// The prefix goes on its own line: a script that opens with a comment or a
// shebang stays a comment, and the shell still execs the last command of the
// script itself, so the game stays PID 1 and gets the stop signal.
func (d *DockerRuntime) shareDataWithGame(cfg *container.Config, host *container.HostConfig, dataPath string, install bool) {
	gid, ok := d.sharedGroup()
	if !ok || len(cfg.Cmd) != 1 {
		return
	}
	host.GroupAdd = append(host.GroupAdd, strconv.Itoa(gid))
	prefix := "umask 0002\n"
	if install {
		prefix += writeProbe(dataPath)
	}
	cfg.Cmd = []string{prefix + cfg.Cmd[0]}
}

// writeProbe is the POSIX shell that fails an install container whose data dir
// it cannot write, naming the uid and groups it ran as.
func writeProbe(dataPath string) string {
	probe := shellQuote(strings.TrimSuffix(dataPath, "/") + "/" + writeProbeName)
	return fmt.Sprintf("if ! ( : > %s ) 2>/dev/null; then "+
		`echo "[kraken] %s is not writable by this container (uid $(id -u), groups $(id -G)); nothing can install into it" >&2; `+
		"exit %d; fi; rm -f %s\n",
		probe, dataPath, exitDataDirNotWritable, probe)
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shareDataDir opens a server's data dir to the shared group: the dir itself,
// and whatever under it this Agent created — a restored save, a rendered
// config, a Files-tab upload. It runs before every container that mounts the
// dir, so anything the Agent wrote while the server was stopped is writable by
// the game when it starts, and a dir created by an older Agent is fixed on its
// next install or start.
//
// What it cannot fix it reports and leaves alone; the install probe is the
// check that counts.
func (d *DockerRuntime) shareDataDir(serverID string) error {
	gid, ok := d.sharedGroup()
	if !ok {
		return nil
	}
	return shareDataTree(d.localDir(serverID), os.Geteuid(), gid)
}

// shareDataDirOrWarn is shareDataDir for a caller that goes ahead regardless.
func (d *DockerRuntime) shareDataDirOrWarn(serverID string, note func(string)) {
	if err := d.shareDataDir(serverID); err != nil {
		slog.Warn("could not open the data dir to the game container's group", "server", serverID, "err", err)
		if note != nil {
			note("could not open the data dir to the game container's group: " + err.Error())
		}
	}
}
