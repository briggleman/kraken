package agent

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

const liveEnshrouded0x6 = "Error! App '2278520' state is 0x6 after update job."

// decodePowerShell reverses encodePowerShellCommand.
func decodePowerShell(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("not a UTF-16LE base64 payload: %v (len %d)", err, len(raw))
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(units))
}

func TestWithContentLogDump_Linux(t *testing.T) {
	script := "steamcmd +force_install_dir /data +login anonymous +app_update 896660 +quit;\n"
	got, ok := withContentLogDump(script, false)
	if !ok {
		t.Fatal("a steamcmd script should get the dump")
	}
	if want := "steamcmd +force_install_dir /data +login anonymous +app_update 896660 +quit;\n" + linuxContentLogDump; got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
	if again, ok := withContentLogDump(got, false); ok || again != got {
		t.Fatalf("the dump went on twice: %q", again)
	}
	if same, ok := withContentLogDump("curl -fsSL https://example.invalid/x.tar.gz | tar xz", false); ok || strings.Contains(same, contentLogBegin) {
		t.Fatalf("a script that never runs steamcmd was changed: %q", same)
	}
}

func TestWithContentLogDump_Windows(t *testing.T) {
	guarded, _ := guardWindowsSteamInstall(`steamcmd.exe +force_install_dir C:\data +login anonymous +app_update 2278520 +quit`)
	got, ok := withContentLogDump(guarded, true)
	if !ok || !strings.HasPrefix(got, guarded+" & ") || !strings.HasSuffix(got, windowsContentLogDump) {
		t.Fatalf("want the dump as the last link after the guard's wait, got %q", got)
	}
	if again, ok := withContentLogDump(got, true); ok || again != got {
		t.Fatalf("the dump went on twice: %q", again)
	}
	// Docker's Windows argument escaping turns " into \" — the dump, like the
	// guard's wait, must carry none.
	if strings.Contains(windowsContentLogDump, `"`) {
		t.Fatalf("dump contains a double quote: %q", windowsContentLogDump)
	}
	payload := decodePowerShell(t, strings.TrimPrefix(windowsContentLogDump, "powershell -NoProfile -NonInteractive -EncodedCommand "))
	for _, want := range []string{contentLogBegin, contentLogEnd, `C:\steamcmd\logs\content_log.txt`, "-Tail " + contentLogTail, "$ProgressPreference = 'SilentlyContinue'"} {
		if !strings.Contains(payload, want) {
			t.Errorf("decoded dump %q lacks %q", payload, want)
		}
	}
}

// The Linux dump must not swallow the script's exit status: a failing install
// still fails the pass.
func TestLinuxContentLogDump_KeepsExitStatus(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	for script, want := range map[string]int{"true": 0, "false": 1, "sh -c 'exit 7'": 7} {
		cmd := exec.Command(sh, "-c", script+"\n"+linuxContentLogDump)
		out, _ := cmd.Output()
		if got := cmd.ProcessState.ExitCode(); got != want {
			t.Errorf("%q: exit %d, want %d", script, got, want)
		}
		if !strings.Contains(string(out), contentLogBegin) || !strings.Contains(string(out), contentLogEnd) {
			t.Errorf("%q: markers missing from %q", script, out)
		}
	}
}

func TestContentLogCapture(t *testing.T) {
	var c contentLogCapture
	stream := []string{
		"Success! App '1' fully installed.",
		"",
		contentLogBegin,
		"[2026-10-15 11:45:54] AppID 2278520 update canceled : Failed to get manifest\r",
		"",
		contentLogEnd,
		"after",
	}
	var passed []string
	for _, l := range stream {
		if rest, ok := c.take(l); ok {
			passed = append(passed, rest)
		}
	}
	// The blank installer line before the dump is kept, as the console always
	// showed it; the blank line inside the dump is not.
	if len(passed) != 3 || passed[0] != stream[0] || passed[1] != "" || passed[2] != "after" {
		t.Fatalf("only the lines outside the markers should pass through: %q", passed)
	}
	got := c.relay()
	if len(got) != 2 || got[1] != contentLogPrefix+"[2026-10-15 11:45:54] AppID 2278520 update canceled : Failed to get manifest" {
		t.Fatalf("relay = %q", got)
	}
	if empty := (&contentLogCapture{}).relay(); len(empty) != 1 || !strings.Contains(empty[0], "no SteamCMD content_log.txt lines") {
		t.Fatalf("an empty capture should say so: %q", empty)
	}
}

// SteamCMD's last line often lacks a newline, so the begin marker arrives
// glued to it (seen live on Linux). The text before the marker is still the
// installer's; nothing of the dump may leak, and nothing of it may be lost.
func TestContentLogCapture_MarkerGluedToSteamCMDsLastLine(t *testing.T) {
	var c contentLogCapture
	stream := []string{
		"Unloading Steam API...OK" + contentLogBegin,
		"[2026-10-15 13:02:11] AuthenticateDepotID 1963721 : Success!",
		"[2026-10-15 13:02:12] AppID 1963720 update canceled : Failed downloading 1 manifests (No connection)" + contentLogEnd,
	}
	var passed []string
	for _, l := range stream {
		if rest, ok := c.take(l); ok {
			passed = append(passed, rest)
		}
	}
	if len(passed) != 1 || passed[0] != "Unloading Steam API...OK" {
		t.Fatalf("want only SteamCMD's own text before the marker to pass through, got %q", passed)
	}
	if len(c.lines) != 2 || !strings.Contains(c.lines[1], "update canceled") {
		t.Fatalf("both log lines should be captured, the last one glued to the end marker too: %q", c.lines)
	}
	// A glued marker with only whitespace before it leaves nothing to emit.
	if rest, ok := (&contentLogCapture{}).take("  " + contentLogBegin); ok {
		t.Fatalf("whitespace before the marker is not installer output: %q", rest)
	}
}

// demux scans stdout and stderr on their own goroutines; the callback must
// still be called one line at a time, since callers fold lines into state.
func TestDemux_SerializesTheCallback(t *testing.T) {
	var buf bytes.Buffer
	out, errw := stdcopy.NewStdWriter(&buf, stdcopy.Stdout), stdcopy.NewStdWriter(&buf, stdcopy.Stderr)
	for i := 0; i < 500; i++ {
		_, _ = fmt.Fprintf(out, "out %d\n", i)
		_, _ = fmt.Fprintf(errw, "err %d\n", i)
	}
	var inside, peak, n int32
	err := demux(&buf, func(_, _ string) error {
		if v := atomic.AddInt32(&inside, 1); v > atomic.LoadInt32(&peak) {
			atomic.StoreInt32(&peak, v)
		}
		time.Sleep(10 * time.Microsecond)
		atomic.AddInt32(&inside, -1)
		n++ // a plain write: -race flags it if two calls overlap
		return nil
	})
	if err != nil || n != 1000 {
		t.Fatalf("demux: err=%v lines=%d", err, n)
	}
	if peak != 1 {
		t.Fatalf("the callback ran %d at once", peak)
	}
}

// Install end to end: the script carries the dump; on a failed pass the
// content log reaches the console as system lines and the verdict is still
// the console's own failure line — a "Success!" inside the content log must
// not clear it.
func TestDockerInstall_RelaysContentLogOnFailure(t *testing.T) {
	d := newFileOpsRuntime(t)
	d.images = &fakeImages{local: map[string]image.InspectResponse{testRef: {ID: oldID}}}
	d.pullPolicy = pullNever
	ops := &fakeOps{passLogs: [][]string{{
		" Update state (0x3) reconfiguring, progress: 0.00 (0 / 0)",
		liveEnshrouded0x6,
		contentLogBegin,
		"[2026-10-15 11:45:54] AppID 2278520 update canceled : Failed to get manifest",
		"[2026-10-15 11:45:54] Success! App '2278520' fully installed.",
		contentLogEnd,
	}}}
	d.containers = ops

	var failed string
	var lines []*agentpb.InstallEvent
	ctx := deadlineCtx(t, 30*time.Minute)
	_ = d.Install(ctx, &agentpb.InstallServerRequest{ServerId: guardServer, Image: testRef, InstallScript: "steamcmd +app_update 2278520 +quit"},
		func(ev *agentpb.InstallEvent) error {
			switch e := ev.Event.(type) {
			case *agentpb.InstallEvent_Failed:
				failed = e.Failed
			case *agentpb.InstallEvent_LogLine:
				lines = append(lines, ev)
			}
			return nil
		})
	d.installCleanup.Wait()

	if !strings.Contains(failed, "state is 0x6") {
		t.Fatalf("want the 0x6 verdict, got %q", failed)
	}
	if len(ops.cmds) == 0 || !strings.HasSuffix(ops.cmds[0][0], linuxContentLogDump) {
		t.Fatalf("the install script should end with the dump: %q", ops.cmds)
	}
	var relayed bool
	for _, ev := range lines {
		text := ev.GetLogLine()
		if strings.Contains(text, contentLogBegin) || strings.Contains(text, contentLogEnd) {
			t.Errorf("a marker reached the console: %q", text)
		}
		if strings.Contains(text, "update canceled") {
			relayed = true
			if !ev.GetSystem() || !strings.HasPrefix(text, contentLogPrefix) {
				t.Errorf("content-log line should be a prefixed system line: %q system=%v", text, ev.GetSystem())
			}
		}
	}
	if !relayed {
		t.Fatalf("the content log never reached the console: %+v", lines)
	}
}

// A clean pass keeps the console as it was: the content log is dropped.
func TestDockerInstall_DropsContentLogOnSuccess(t *testing.T) {
	d := newFileOpsRuntime(t)
	d.images = &fakeImages{local: map[string]image.InspectResponse{testRef: {ID: oldID}}}
	d.pullPolicy = pullNever
	d.containers = &fakeOps{passLogs: [][]string{{
		"Success! App '2278520' fully installed.",
		contentLogBegin,
		"[2026-10-15 11:47:46] AppID 2278520 finished update (BuildID 25936465 => 25936466)",
		contentLogEnd,
	}}}

	var completed bool
	ctx := deadlineCtx(t, 30*time.Minute)
	err := d.Install(ctx, &agentpb.InstallServerRequest{ServerId: guardServer, Image: testRef, InstallScript: "steamcmd"},
		func(ev *agentpb.InstallEvent) error {
			switch e := ev.Event.(type) {
			case *agentpb.InstallEvent_Completed:
				completed = true
			case *agentpb.InstallEvent_LogLine:
				if strings.Contains(e.LogLine, "content_log") || strings.Contains(e.LogLine, "finished update") {
					t.Errorf("a clean pass printed content-log output: %q", e.LogLine)
				}
			}
			return nil
		})
	d.installCleanup.Wait()
	if err != nil || !completed {
		t.Fatalf("want a completed install, got err=%v", err)
	}
}
