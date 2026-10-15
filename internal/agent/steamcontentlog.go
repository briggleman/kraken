package agent

import (
	"strings"
)

// SteamCMD's console says only THAT an app_update failed — "Error! App '…'
// state is 0x6 after update job." — never why. The why goes to
// logs/content_log.txt under SteamCMD's own root ("update canceled : …", the
// depot and manifest it was fetching, the HTTP status the content server
// answered with). That root is inside the install container, so the file used
// to be thrown away with it: the 2026-10-15 enshrouded-01 0x6 left nothing to
// read but the console line.
//
// So every SteamCMD install script ends by printing the tail of content_log.txt
// between two marker lines. The Agent takes those lines out of the stream —
// they never reach the console as installer output, and SteamCMD's own words
// in them ("Failed", "Error") can never be mistaken for the verdict — and puts
// them on the console as system lines only when the pass failed. A clean pass
// looks exactly as it did before.

const (
	contentLogBegin = "---kraken-steamcmd-content-log-begin---"
	contentLogEnd   = "---kraken-steamcmd-content-log-end---"

	// contentLogTail is how many lines of content_log.txt the script prints. A
	// pass that fails at once logs a few dozen; a long download logs a line per
	// state change, and the reason for a failure is at the end.
	contentLogTail = "120"

	// contentLogPrefix opens each relayed line on the console.
	contentLogPrefix = "[kraken] steamcmd content_log: "
)

// windowsContentLogDump prints C:\steamcmd\logs\content_log.txt (the steam-win
// image's SteamCMD root) between the markers. It goes through -EncodedCommand
// for the same reason as the relaunch guard's wait (see steamguard.go):
// Docker's Windows argument escaping mangles quotes in a plain -Command.
// $ProgressPreference keeps PowerShell from writing a CLIXML progress record
// to stderr, which would land on the console as noise.
var windowsContentLogDump = `powershell -NoProfile -NonInteractive -EncodedCommand ` + encodePowerShellCommand(
	`$ProgressPreference = 'SilentlyContinue'; `+
		`Write-Output '`+contentLogBegin+`'; `+
		`$f = 'C:\steamcmd\logs\content_log.txt'; `+
		`if (Test-Path $f) { Get-Content $f -Tail `+contentLogTail+` }; `+
		`Write-Output '`+contentLogEnd+`'`)

// linuxContentLogDump prints the content log of the steam-base / steam-wine
// SteamCMD. The binaries live in /home/steam/steamcmd (from steam-deps), but
// Linux SteamCMD writes its logs under $HOME/Steam — found with `find` after a
// real app_update on kraken-steam-base, 2026-10-15. The steamcmd dir is kept as
// a fallback for an image that roots SteamCMD there. It keeps the script's own
// exit status: `rc=$?` takes it from the script's last command, and the dump
// exits with it, so a failing script still fails the pass.
const linuxContentLogDump = `rc=$?; echo '` + contentLogBegin + `'; ` +
	`for f in "$HOME/Steam/logs/content_log.txt" /home/steam/steamcmd/logs/content_log.txt; do ` +
	`if [ -f "$f" ]; then tail -n ` + contentLogTail + ` "$f"; break; fi; done; ` +
	`echo '` + contentLogEnd + `'; exit $rc`

// withContentLogDump appends the content-log dump to an install script that
// runs SteamCMD. A script that never mentions steamcmd, or already carries the
// dump, is returned unchanged.
//
// On Windows it is one more `&` link in the cmd chain, after the relaunch
// guard's trailing wait, so the relaunched SteamCMD has finished writing the
// file. cmd's exit status is already the wait's there, so the dump changes
// nothing about it. On Linux it goes on a line of its own, so it follows a
// script ending in a comment or a newline as surely as one ending in `;`.
func withContentLogDump(script string, windows bool) (string, bool) {
	if !strings.Contains(strings.ToLower(script), "steamcmd") || strings.Contains(script, contentLogEndMarkerIn(windows)) {
		return script, false
	}
	if windows {
		return strings.TrimRight(script, " \r\n") + " & " + windowsContentLogDump, true
	}
	return strings.TrimRight(script, "\r\n") + "\n" + linuxContentLogDump, true
}

// contentLogEndMarkerIn is what marks a script as already carrying the dump:
// the Windows dump is encoded, so its marker is the encoded command itself.
func contentLogEndMarkerIn(windows bool) string {
	if windows {
		return windowsContentLogDump
	}
	return contentLogEnd
}

// contentLogCapture lifts the dump out of an install container's log stream.
type contentLogCapture struct {
	in    bool
	lines []string
}

// take reports whether text belongs to the dump — a marker or a line between
// them — and keeps the lines. Everything else is the installer's own output.
func (c *contentLogCapture) take(text string) bool {
	switch strings.TrimSpace(text) {
	case contentLogBegin:
		c.in = true
		c.lines = c.lines[:0]
		return true
	case contentLogEnd:
		c.in = false
		return true
	}
	if !c.in {
		return false
	}
	if line := strings.TrimRight(text, "\r\n"); strings.TrimSpace(line) != "" {
		c.lines = append(c.lines, line)
	}
	return true
}

// relay renders the captured lines for the console of a failed pass, with a
// heading that says where they came from. Nothing captured — an older image,
// a script cut short before the dump, a SteamCMD that wrote no content log —
// is said too, so an operator does not go looking for lines that never came.
func (c *contentLogCapture) relay() []string {
	if len(c.lines) == 0 {
		return []string{"[kraken] no SteamCMD content_log.txt lines came back from this pass"}
	}
	out := make([]string, 0, len(c.lines)+1)
	out = append(out, "[kraken] SteamCMD's content_log.txt from this pass (the reason behind the failure is usually near the end):")
	for _, l := range c.lines {
		out = append(out, contentLogPrefix+l)
	}
	return out
}
