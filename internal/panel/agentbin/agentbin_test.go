package agentbin

import (
	"errors"
	"testing"
)

// TestNaming locks the embedded file naming to the release-asset convention —
// the CI step and Makefile target write these exact paths.
func TestNaming(t *testing.T) {
	cases := map[[2]string]string{
		{"linux", "amd64"}:   "dist/kraken-agent-linux-amd64",
		{"linux", "arm64"}:   "dist/kraken-agent-linux-arm64",
		{"windows", "amd64"}: "dist/kraken-agent-windows-amd64.exe",
	}
	for in, want := range cases {
		if got := name(in[0], in[1]); got != want {
			t.Errorf("name(%s, %s) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// TestGetHonestAboutAbsence — Get for a platform that can never be embedded
// must return ErrNotEmbedded (the dev-build / unsupported-platform path).
func TestGetHonestAboutAbsence(t *testing.T) {
	if _, _, err := Get("plan9", "mips"); !errors.Is(err, ErrNotEmbedded) {
		t.Errorf("Get(plan9/mips) err = %v, want ErrNotEmbedded", err)
	}
	if Has("plan9", "mips") {
		t.Error("Has(plan9/mips) = true")
	}
	// When a real binary IS embedded (local `make embed-agents` ran), Get must
	// agree with Has and return non-empty data + a 64-hex checksum.
	if Has("linux", "amd64") {
		data, sha, err := Get("linux", "amd64")
		if err != nil || len(data) == 0 || len(sha) != 64 {
			t.Errorf("Get(linux/amd64): len=%d sha=%q err=%v", len(data), sha, err)
		}
	}
}
