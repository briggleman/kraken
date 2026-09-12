package agent

import (
	"encoding/base64"
	"strings"
	"unicode/utf16"
)

// SteamCMD self-update relaunch guard (Windows containers only).
//
// On Windows, steamcmd.exe does not update itself in place: when Valve ships a
// newer client the running bootstrapper SPAWNS the new binary and exits. Inside
// a container that is fatal. The agent runs a windows-native install script as
// `cmd /S /C "<script>"`, so cmd is PID 1: it sees the first steamcmd.exe
// return, walks the rest of the `&` chain, reaches the end — and the container
// dies while the relaunched downloader is still writing steamapps/downloading.
// The install exits 0 with no game files, nothing that steamInstallFailureRE
// matches, and the Panel marks the server offline over an empty data dir.
// Observed live on abyss-win on 2026-09-12 (issue #278).
//
// The guard rewrites a windows-native install script so that:
//
//  1. a bare `steamcmd.exe +quit` runs first, which is enough to trigger the
//     self-update, and
//  2. every steamcmd invocation — the prime, each real pass, and the end of the
//     chain — is followed by a PowerShell loop that blocks until no steamcmd
//     process is left. The bootstrapper can relaunch itself on any pass, not
//     just the updating one.
//
// The trailing wait is what keeps the container alive long enough for the
// relaunched child's "Success! App … fully installed" line to reach the log
// stream, so the existing success/failure regexes still decide the outcome.
//
// THE ESCAPING GOTCHA: the wait CANNOT be written as `powershell -Command
// "…"`. Docker's Windows argument escaping rewrites inner double quotes as
// \", so PowerShell receives the loop as a quoted string literal, echoes it,
// and exits 0 — silently doing nothing (verified live). It must be passed as
// -EncodedCommand with a UTF-16LE base64 payload, which carries no quotes at
// all. The payload below is computed from readable plain text at init rather
// than hard-coded as an opaque blob.

// steamcmdWaitScript is the PowerShell the guard runs after each steamcmd
// invocation. The leading sleep gives a relaunched bootstrapper time to appear
// in the process table before the loop first samples it.
const steamcmdWaitScript = `Start-Sleep -Seconds 3; while (Get-Process steamcmd -ErrorAction SilentlyContinue) { Start-Sleep -Seconds 2 }`

// steamcmdPrime triggers SteamCMD's self-update on its own, before any real
// app_update pass can be cut short by the relaunch.
const steamcmdPrime = `steamcmd.exe +quit`

var (
	// steamcmdWaitB64 is steamcmdWaitScript as UTF-16LE base64 — the
	// -EncodedCommand payload, and the marker the idempotence check looks for.
	steamcmdWaitB64 = encodePowerShellCommand(steamcmdWaitScript)

	// steamcmdWaitCmd is the full cmd-level command that waits for steamcmd.
	steamcmdWaitCmd = `powershell -NoProfile -NonInteractive -EncodedCommand ` + steamcmdWaitB64
)

// encodePowerShellCommand renders s as PowerShell's -EncodedCommand payload:
// UTF-16LE, base64. Surrogate pairs are handled by utf16.Encode.
func encodePowerShellCommand(s string) string {
	units := utf16.Encode([]rune(s))
	buf := make([]byte, 0, len(units)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// guardWindowsSteamInstall returns script rewritten with the SteamCMD relaunch
// guard, and whether it changed anything. It is a no-op for scripts that never
// mention steamcmd and for scripts that already carry the guard (a spec may
// still ship its own copy — dragonwilds.yaml does). Callers must only apply it
// on the windows-native install path; the Linux bootstrapper updates itself in
// place and needs none of this.
func guardWindowsSteamInstall(script string) (string, bool) {
	if !strings.Contains(strings.ToLower(script), "steamcmd") {
		return script, false
	}
	// Already guarded — by us on an earlier pass, or by the spec itself.
	if strings.Contains(script, steamcmdWaitB64) {
		return script, false
	}

	body := waitAfterEachSteamcmd(strings.TrimSpace(script))

	out := steamcmdPrime + " & " + steamcmdWaitCmd + " & " + body
	if !strings.HasSuffix(out, steamcmdWaitCmd) {
		out += " & " + steamcmdWaitCmd
	}
	return out, true
}

// waitAfterEachSteamcmd inserts a wait after every segment of a cmd chain that
// invokes steamcmd. It returns script untouched when the chain cannot be split
// safely — the prime plus the caller's trailing wait still cover the common
// failure, so bailing out degrades rather than breaks.
func waitAfterEachSteamcmd(script string) string {
	parts, ok := splitCmdChain(script)
	if !ok {
		return script
	}
	out := make([]string, 0, len(parts)*2)
	for _, p := range parts {
		out = append(out, p)
		if strings.Contains(strings.ToLower(p), "steamcmd") {
			out = append(out, steamcmdWaitCmd)
		}
	}
	return strings.Join(out, " & ")
}

// splitCmdChain splits a cmd.exe command chain on its single-`&` separators,
// reporting false when naive splitting would be wrong. It refuses a script
// containing:
//
//   - `"` — an `&` inside a quoted argument is not a separator;
//   - `^` — cmd's escape character, which can hide an `&`;
//   - `(` / `)` — command grouping, where an `&` binds to the group;
//   - `<` / `>` — redirection, since `2>&1` embeds a non-separator `&`;
//   - `|` — pipes and `||`, whose control flow we would have to preserve;
//   - `&&` — conditional chaining, where inserting a wait between the two
//     halves would replace the left command's exit code with the wait's.
func splitCmdChain(script string) ([]string, bool) {
	if strings.ContainsAny(script, "\"^()<>|") {
		return nil, false
	}
	var parts []string
	start := 0
	for i := 0; i < len(script); i++ {
		if script[i] != '&' {
			continue
		}
		end := i
		for end < len(script) && script[end] == '&' {
			end++
		}
		if end-i != 1 { // `&&` (or worse) — bail out entirely
			return nil, false
		}
		parts = append(parts, script[start:i])
		start = end
		i = end - 1
	}
	parts = append(parts, script[start:])

	trimmed := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			trimmed = append(trimmed, p)
		}
	}
	if len(trimmed) == 0 {
		return nil, false
	}
	return trimmed, true
}
