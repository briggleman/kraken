package agent

import "testing"

// TestSteamInstallFailureRE locks the detection of SteamCMD app failures that the
// process exit code hides (SteamCMD exits 0 even when an app fails to download).
func TestSteamInstallFailureRE(t *testing.T) {
	failures := []string{
		"ERROR! Failed to install app '1829350' (Missing configuration)",
		"ERROR! Failed to install app '740' (No subscription)",
		"Not for anonymous users.",
	}
	for _, line := range failures {
		if !steamInstallFailureRE.MatchString(line) {
			t.Errorf("expected failure match for %q", line)
		}
	}

	ok := []string{
		"Success! App '1007' fully installed.",
		" Update state (0x61) downloading, progress: 12.34 (100 / 810)",
		"[ 50%] Downloading update (40,640 of 43,472 KB)...",
		"Connecting anonymously to Steam Public...OK",
	}
	for _, line := range ok {
		if steamInstallFailureRE.MatchString(line) {
			t.Errorf("did not expect failure match for %q", line)
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
