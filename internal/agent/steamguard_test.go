package agent

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

// The -EncodedCommand payload must decode back to the readable loop; if this
// drifts, operators reading the install log see an opaque blob that does
// something else.
func TestSteamcmdWaitPayloadDecodes(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(steamcmdWaitB64)
	if err != nil {
		t.Fatalf("payload is not valid base64: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("payload is not UTF-16LE: odd length %d", len(raw))
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	got := string(utf16.Decode(units))
	want := `Start-Sleep -Seconds 3; while (Get-Process steamcmd -ErrorAction SilentlyContinue) { Start-Sleep -Seconds 2 }`
	if got != want {
		t.Fatalf("decoded payload\n got: %q\nwant: %q", got, want)
	}
	// The whole point of -EncodedCommand is that no quote survives to be
	// mangled into \" by Docker's Windows argument escaping.
	if strings.ContainsAny(steamcmdWaitCmd, `"`) {
		t.Fatalf("wait command contains a double quote: %q", steamcmdWaitCmd)
	}
	// dragonwilds.yaml ships this exact payload inline (it predates the agent
	// guard). Matching it byte-for-byte is what makes the idempotence check
	// recognise that spec's hand-written loop and leave the script alone.
	const dragonwildsPayload = `UwB0AGEAcgB0AC0AUwBsAGUAZQBwACAALQBTAGUAYwBvAG4AZABzACAAMwA7ACAAdwBoAGkAbABlACAAKABHAGUAdAAtAFAAcgBvAGMAZQBzAHMAIABzAHQAZQBhAG0AYwBtAGQAIAAtAEUAcgByAG8AcgBBAGMAdABpAG8AbgAgAFMAaQBsAGUAbgB0AGwAeQBDAG8AbgB0AGkAbgB1AGUAKQAgAHsAIABTAHQAYQByAHQALQBTAGwAZQBlAHAAIAAtAFMAZQBjAG8AbgBkAHMAIAAyACAAfQA=`
	if steamcmdWaitB64 != dragonwildsPayload {
		t.Fatalf("payload drifted from the one bundled in dragonwilds.yaml:\n got: %s\nwant: %s", steamcmdWaitB64, dragonwildsPayload)
	}
}

func TestGuardWindowsSteamInstall(t *testing.T) {
	wait := steamcmdWaitCmd
	twoPass := `steamcmd.exe +force_install_dir C:\data +login anonymous +app_update 4019830 validate +quit & steamcmd.exe +force_install_dir C:\data +login anonymous +app_update 4019830 validate +quit`

	tests := []struct {
		name    string
		script  string
		want    string
		applied bool
	}{
		{
			name:    "single steamcmd pass gets prime and waits",
			script:  `steamcmd.exe +login anonymous +app_update 1829350 validate +quit`,
			want:    steamcmdPrime + " & " + wait + " & steamcmd.exe +login anonymous +app_update 1829350 validate +quit & " + wait,
			applied: true,
		},
		{
			name:   "two-pass chain gets a wait after each invocation",
			script: twoPass,
			want: steamcmdPrime + " & " + wait +
				` & steamcmd.exe +force_install_dir C:\data +login anonymous +app_update 4019830 validate +quit & ` + wait +
				` & steamcmd.exe +force_install_dir C:\data +login anonymous +app_update 4019830 validate +quit & ` + wait,
			applied: true,
		},
		{
			name:   "non-steam segments are left in place",
			script: `mkdir C:\data\logs & steamcmd.exe +quit & copy C:\seed\* C:\data`,
			want: steamcmdPrime + " & " + wait +
				` & mkdir C:\data\logs & steamcmd.exe +quit & ` + wait +
				` & copy C:\seed\* C:\data & ` + wait,
			applied: true,
		},
		{
			name:    "non-steam script untouched",
			script:  `mkdir C:\data\saves & copy C:\seed\* C:\data`,
			want:    `mkdir C:\data\saves & copy C:\seed\* C:\data`,
			applied: false,
		},
		{
			name:    "case-insensitive match on SteamCMD",
			script:  `SteamCMD.exe +quit`,
			want:    steamcmdPrime + " & " + wait + " & SteamCMD.exe +quit & " + wait,
			applied: true,
		},
		{
			name:    "quoted script falls back to prime plus trailing wait",
			script:  `steamcmd.exe +force_install_dir "C:\data dir" +app_update 1 +quit`,
			want:    steamcmdPrime + " & " + wait + ` & steamcmd.exe +force_install_dir "C:\data dir" +app_update 1 +quit & ` + wait,
			applied: true,
		},
		{
			name:    "conditional && chain falls back rather than break exit-code gating",
			script:  `steamcmd.exe +quit && steamcmd.exe +app_update 1 +quit`,
			want:    steamcmdPrime + " & " + wait + ` & steamcmd.exe +quit && steamcmd.exe +app_update 1 +quit & ` + wait,
			applied: true,
		},
		{
			name:    "redirection falls back (2>&1 embeds a non-separator &)",
			script:  `steamcmd.exe +quit > C:\data\steam.log 2>&1`,
			want:    steamcmdPrime + " & " + wait + ` & steamcmd.exe +quit > C:\data\steam.log 2>&1 & ` + wait,
			applied: true,
		},
		{
			name:    "empty script untouched",
			script:  "",
			want:    "",
			applied: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, applied := guardWindowsSteamInstall(tc.script)
			if applied != tc.applied {
				t.Fatalf("applied = %v, want %v", applied, tc.applied)
			}
			if got != tc.want {
				t.Fatalf("guarded script\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// Re-guarding an already-guarded script must be a no-op — both for our own
// output and for a spec that ships the same prime + -EncodedCommand loop by
// hand (dragonwilds.yaml does).
func TestGuardWindowsSteamInstallIsIdempotent(t *testing.T) {
	scripts := map[string]string{
		"our own output": `steamcmd.exe +login anonymous +app_update 1829350 validate +quit`,
		"spec-authored guard": steamcmdPrime + " & " + steamcmdWaitCmd +
			` & steamcmd.exe +force_install_dir C:\data +login anonymous +app_update 4019830 validate +quit & ` + steamcmdWaitCmd,
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			once, _ := guardWindowsSteamInstall(script)
			twice, applied := guardWindowsSteamInstall(once)
			if applied {
				t.Fatalf("second pass reported a change")
			}
			if twice != once {
				t.Fatalf("second pass rewrote the script\n got: %s\nwant: %s", twice, once)
			}
			if strings.Count(once, steamcmdWaitB64) != strings.Count(twice, steamcmdWaitB64) {
				t.Fatalf("wait count changed on the second pass")
			}
		})
	}
}

// Install gates the guard on isWindows(), so a Linux node's shell install
// script is handed to /bin/sh exactly as the spec wrote it. Assert the gate
// itself, and show that a Linux script IS something the transform would
// rewrite — i.e. the gate is load-bearing, not incidental.
func TestGuardIsGatedOnWindowsRuntime(t *testing.T) {
	if (&DockerRuntime{osType: "linux"}).isWindows() {
		t.Fatalf("linux runtime reported windows")
	}
	if !(&DockerRuntime{osType: "windows"}).isWindows() {
		t.Fatalf("windows runtime did not report windows")
	}

	linuxScript := `steamcmd +force_install_dir /data +login anonymous +app_update 1829350 validate +quit; ` +
		`steamcmd +force_install_dir /data +login anonymous +app_update 1829350 validate +quit`
	if _, applied := guardWindowsSteamInstall(linuxScript); !applied {
		t.Fatalf("expected the transform to rewrite a bare steamcmd script; the isWindows() gate must be what spares Linux")
	}
}
