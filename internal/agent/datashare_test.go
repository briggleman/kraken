package agent

import (
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// pinAgentGroup makes agentGroupID report gid for the length of the test.
func pinAgentGroup(t *testing.T, gid int, ok bool) {
	t.Helper()
	prev := agentGroupID
	agentGroupID = func() (int, bool) { return gid, ok }
	t.Cleanup(func() { agentGroupID = prev })
}

func TestShareDataWithGame(t *testing.T) {
	const script = "#!/bin/sh\nsteamcmd +quit"
	cases := []struct {
		name      string
		osType    string
		gid       int
		groupOK   bool
		install   bool
		wantGroup []string
		wantProbe bool
	}{
		{name: "linux install as the kraken user", osType: "linux", gid: 989, groupOK: true, install: true, wantGroup: []string{"989"}, wantProbe: true},
		{name: "linux runtime", osType: "linux", gid: 989, groupOK: true, wantGroup: []string{"989"}},
		{name: "linux install as root", osType: "linux", gid: 0, groupOK: true, install: true, wantGroup: []string{"0"}, wantProbe: true},
		{name: "windows node", osType: "windows", gid: 989, groupOK: true, install: true},
		{name: "agent on a windows host", osType: "linux", groupOK: false, install: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinAgentGroup(t, tc.gid, tc.groupOK)
			d := &DockerRuntime{osType: tc.osType}
			cfg := &container.Config{Cmd: []string{script}}
			host := &container.HostConfig{}
			d.shareDataWithGame(cfg, host, "/data", tc.install)

			if strings.Join(host.GroupAdd, ",") != strings.Join(tc.wantGroup, ",") {
				t.Fatalf("GroupAdd = %v, want %v", host.GroupAdd, tc.wantGroup)
			}
			cmd := cfg.Cmd[0]
			if tc.wantGroup == nil {
				if cmd != script {
					t.Fatalf("command changed where nothing is shared:\n%s", cmd)
				}
				return
			}
			if !strings.HasPrefix(cmd, "umask 0002\n") {
				t.Fatalf("command does not open with the umask:\n%s", cmd)
			}
			if !strings.HasSuffix(cmd, "\n"+script) {
				t.Fatalf("the spec's script is not left whole on its own lines:\n%s", cmd)
			}
			if got := strings.Contains(cmd, writeProbeName); got != tc.wantProbe {
				t.Fatalf("write probe present = %v, want %v:\n%s", got, tc.wantProbe, cmd)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/data/x":     `'/data/x'`,
		"/data/it's":  `'/data/it'\''s'`,
		"/data/a b$c": `'/data/a b$c'`,
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
