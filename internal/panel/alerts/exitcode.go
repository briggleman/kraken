package alerts

import "fmt"

// exitHints are the Windows NTSTATUS codes an operator actually meets when a
// game server dies in a container — the same four the drill-in explains
// (EXIT_HINTS in web/src/lib/fmt.ts, which a test holds this table to). They
// are worded for a lock screen: what went wrong, short enough to read there.
var exitHints = map[uint32]string{
	0xC0000135: "a DLL the game needs is missing",
	0xC0000139: "a DLL is the wrong build (an export the game wants is missing)",
	0xC0000005: "an access violation (the game crashed on a bad memory access)",
	0xC000007B: "bad image format (a 32/64-bit mismatch between the game and a DLL)",
}

// ExplainExit renders an exit code the way the drill-in does, shortened for a
// notification: "exit 0xC0000135, a DLL the game needs is missing".
//
// A Linux exit status is 0–255 and stays decimal. Anything outside that range
// is a Windows code, which is only recognisable in hex — 3221225781 is noise,
// 0xC0000135 is a name — and is rendered as the unsigned 32-bit value, so a code
// that reached the Panel sign-extended still matches its documentation. 129 to
// 164 is a process killed by a signal, the way a shell reports it.
func ExplainExit(code int64) string {
	if code < 0 || code > 255 {
		hex := uint32(code) // #nosec G115 -- NTSTATUS codes are 32-bit; this is the documented form
		line := fmt.Sprintf("exit 0x%08X", hex)
		if hint, ok := exitHints[hex]; ok {
			return line + ", " + hint
		}
		return line
	}
	line := fmt.Sprintf("exit %d", code)
	switch {
	case code == 0:
		return line + ", the process ended on its own without an error code"
	case code > 128 && code < 165:
		return fmt.Sprintf("%s, killed by signal %d", line, code-128)
	}
	return line
}
