package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// An install pass writes two kinds of line: what the installer printed, and
// what the Agent did around it — the image check, the guard, the script's run
// time. The Panel's console sets the second apart by the `system` flag (#392),
// so every Agent-authored line must carry it and no installer line may.
func TestDockerInstall_AgentLinesAreSystemLines(t *testing.T) {
	d := newFileOpsRuntime(t)
	d.images = &fakeImages{local: map[string]image.InspectResponse{testRef: {ID: oldID}}}
	d.pullPolicy = pullNever
	ops := &fakeOps{passLogs: [][]string{
		{" Update state (0x61) downloading, progress: 12.34 (100 / 810)", "Success! App '1' fully installed."},
	}}
	d.containers = ops

	type line struct {
		text   string
		system bool
	}
	var lines []line
	var completed bool
	ctx := deadlineCtx(t, 30*time.Minute)
	err := d.Install(ctx, &agentpb.InstallServerRequest{ServerId: guardServer, Image: testRef, InstallScript: "steamcmd"},
		func(ev *agentpb.InstallEvent) error {
			switch e := ev.Event.(type) {
			case *agentpb.InstallEvent_LogLine:
				lines = append(lines, line{e.LogLine, ev.GetSystem()})
			case *agentpb.InstallEvent_Completed:
				completed = true
			case *agentpb.InstallEvent_Failed:
				t.Fatalf("install failed: %s", e.Failed)
			}
			return nil
		})
	if err != nil || !completed {
		t.Fatalf("want a completed install, got err=%v completed=%v", err, completed)
	}
	d.installCleanup.Wait()

	var installer, system int
	for _, l := range lines {
		prefixed := strings.HasPrefix(l.text, "[kraken] ")
		if prefixed != l.system {
			t.Errorf("line %q: system=%v, but the [kraken] prefix says %v — the flag and the prefix must agree", l.text, l.system, prefixed)
		}
		if l.system {
			system++
		} else {
			installer++
		}
	}
	if installer != 2 {
		t.Errorf("want the two installer lines on the ordinary stream, got %d: %+v", installer, lines)
	}
	if system == 0 {
		t.Fatalf("no system lines at all: %+v", lines)
	}

	// The step lines an operator reads to see where the time went.
	for _, want := range []string{
		"[kraken] Using local image ", // the image check, before the script
		"[kraken] image check took ",
		"[kraken] running the install script",
		"[kraken] install script ran ", // with its exit code
	} {
		found := false
		for _, l := range lines {
			if strings.HasPrefix(l.text, want) && l.system {
				found = true
				if want == "[kraken] install script ran " && !strings.HasSuffix(l.text, ", exit 0") {
					t.Errorf("the script's run-time line should end with its exit code: %q", l.text)
				}
			}
		}
		if !found {
			t.Errorf("no system line starting %q; lines: %+v", want, lines)
		}
	}
}

// sysLine adds the prefix when the caller left it off and leaves it alone when
// the caller (the guard's and the recovery's notes) already wrote it.
func TestSysLine_PrefixOnce(t *testing.T) {
	for in, want := range map[string]string{
		"image check took 2s":          "[kraken] image check took 2s",
		"[kraken] removed container x": "[kraken] removed container x",
	} {
		ev := sysLine(in)
		if got := ev.GetLogLine(); got != want || !ev.GetSystem() {
			t.Errorf("sysLine(%q) = %q system=%v, want %q system=true", in, got, ev.GetSystem(), want)
		}
	}
}

// took reads as a duration, not a stopwatch: seconds once past one,
// milliseconds under that so a fast phase does not print as 0s.
func TestTook_Rounds(t *testing.T) {
	if got := took(time.Now().Add(-400 * time.Minute)); got != "6h40m0s" {
		t.Errorf("took(6h40m) = %q", got)
	}
	if got := took(time.Now().Add(-6*time.Minute - 2*time.Second - 400*time.Millisecond)); got != "6m2s" {
		t.Errorf("took(6m2.4s) = %q", got)
	}
	if got := took(time.Now().Add(-120 * time.Millisecond)); !strings.HasSuffix(got, "ms") {
		t.Errorf("took(120ms) = %q, want milliseconds", got)
	}
}
