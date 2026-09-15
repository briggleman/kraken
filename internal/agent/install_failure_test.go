package agent

import "testing"

// TestSteamInstallFailureRE locks the detection of SteamCMD app failures that the
// process exit code hides (SteamCMD exits 0 even when an app fails to download).
func TestSteamInstallFailureRE(t *testing.T) {
	failures := []struct {
		name string
		line string
	}{
		{"failed to install app", "ERROR! Failed to install app '1829350' (Missing configuration)"},
		{"no subscription", "ERROR! Failed to install app '740' (No subscription)"},
		{"anonymous", "Not for anonymous users."},
		// The line observed live on 2026-09-15 (Dragonwilds, app 4019830): both
		// app_update passes printed it and exited 0, so the Agent reported the
		// install complete and the Panel relaunched the stale build.
		{"state 0x6", "Error! App '4019830' state is 0x6 after update job."},
		{"state 0x212 disk space", "Error! App '232250' state is 0x212 after update job."},
		{"state without app id", "Error! State is 0x402 after update job."},
		{"state with steamcmd's is-is typo", "Error! App '4019830' state is is 0x2 after update job."},
		{"branch password", "ERROR! Password check for AppId 4019830 returned error Failure."},
		{"missing update files", "ERROR! Failed to install app '232250' (Missing update files)"},
		{"corrupt update files", "ERROR! Failed to install app '317670' (Corrupt update files)"},
		{"invalid platform", "ERROR! Failed to install app '4019830' (Invalid platform)"},
		{"rate limit", "ERROR! Failed to install app '4019830' (Rate Limit Exceeded)"},
		{"workshop timeout", "ERROR! Timeout downloading item 1234567890"},
		{"disk write failure", "ERROR! Failed to install app '4019830' (Disk write failure)"},
		{"depot download failed", "Error! Depot download failed : 4019831"},
	}
	for _, tc := range failures {
		if !steamInstallFailureRE.MatchString(tc.line) {
			t.Errorf("%s: expected failure match for %q", tc.name, tc.line)
		}
	}

	ok := []struct {
		name string
		line string
	}{
		{"success", "Success! App '1007' fully installed."},
		{"downloading", " Update state (0x61) downloading, progress: 12.34 (100 / 810)"},
		// The two progress lines that preceded the live 0x6 failure — neither may
		// be mistaken for the "state is 0x… after update job" error line.
		{"reconfiguring", " Update state (0x3) reconfiguring, progress: 0.00 (0 / 0)"},
		{"unknown state", " Update state (0x0) unknown, progress: 0.00 (0 / 0)"},
		{"validating", " Update state (0x5) validating, progress: 13.48 (200 / 1483)"},
		{"percent", "[ 50%] Downloading update (40,640 of 43,472 KB)..."},
		{"connecting", "Connecting anonymously to Steam Public...OK"},
		{"kraken marker", "[kraken] installing factorio stable (installed: none)"},
		{"panel marker", "[panel] update complete — starting"},
	}
	for _, tc := range ok {
		if steamInstallFailureRE.MatchString(tc.line) {
			t.Errorf("%s: did not expect failure match for %q", tc.name, tc.line)
		}
	}

	// The success line must match the success regex so it can clear a prior
	// transient failure (the "Missing configuration" two-step).
	if !steamInstallSuccessRE.MatchString("Success! App '1829350' fully installed.") {
		t.Error("expected success regex to match the fully-installed line")
	}
	if steamInstallSuccessRE.MatchString("ERROR! Failed to install app '1829350' (Missing configuration)") {
		t.Error("success regex must not match a failure line")
	}
}

// TestSteamInstallOutcome locks the last-relevant-line-wins folding the install
// log stream applies: a success clears an earlier failure, a later failure beats
// an earlier success.
func TestSteamInstallOutcome(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name:  "clean install",
			lines: []string{"Connecting anonymously to Steam Public...OK", " Update state (0x61) downloading, progress: 1.11 (1 / 90)", "Success! App '4019830' fully installed."},
			want:  "",
		},
		{
			name: "two-step: transient missing configuration then success",
			lines: []string{
				"ERROR! Failed to install app '4019830' (Missing configuration)",
				" Update state (0x61) downloading, progress: 42.00 (420 / 1000)",
				"Success! App '4019830' fully installed.",
			},
			want: "",
		},
		{
			name: "live regression: both passes end in state 0x6",
			lines: []string{
				" Update state (0x3) reconfiguring, progress: 0.00 (0 / 0)",
				" Update state (0x0) unknown, progress: 0.00 (0 / 0)",
				"Error! App '4019830' state is 0x6 after update job.",
			},
			want: "Error! App '4019830' state is 0x6 after update job.",
		},
		{
			name: "second pass fails after the first succeeded",
			lines: []string{
				"Success! App '4019830' fully installed.",
				" Update state (0x3) reconfiguring, progress: 0.00 (0 / 0)",
				"Error! App '4019830' state is 0x6 after update job.",
			},
			want: "Error! App '4019830' state is 0x6 after update job.",
		},
		{
			name: "no steam lines at all",
			lines: []string{
				"[kraken] installing factorio stable (installed: none)",
				"[panel] update complete — starting",
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			for _, line := range tc.lines {
				got = steamInstallOutcome(got, line)
			}
			if got != tc.want {
				t.Errorf("steamInstallOutcome = %q, want %q", got, tc.want)
			}
		})
	}
}
