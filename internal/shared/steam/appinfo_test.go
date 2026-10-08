package steam

import (
	"os"
	"strings"
	"testing"
)

// appInfoFixture is the raw stdout of
//
//	steamcmd +login anonymous +app_info_update 1 +app_info_print <id> … +quit
//
// for the seven bundled Steam apps, captured on 2026-10-08 from
// ghcr.io/briggleman/kraken-steam-base:latest. It carries everything the
// parser has to get past: SteamCMD's self-update preamble, the ANSI resets
// (one at the start of the first header line), depot manifests keyed
// "public" and "default_old" that are NOT branches, and Valheim's six
// non-public branches beside public.
func appInfoFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/appinfo_7apps.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !strings.Contains(string(b), "\x1b[0mAppID : 2394010") {
		t.Fatal("the fixture lost the ANSI reset before the first header; recapture it raw (no terminal, no editor)")
	}
	return string(b)
}

func TestParseAppInfo_SevenBundledApps(t *testing.T) {
	apps, err := ParseAppInfo(appInfoFixture(t))
	if err != nil {
		t.Fatalf("ParseAppInfo: %v", err)
	}
	tests := []struct {
		appID, name   string
		change        int64
		build         string
		updated, made int64
		branches      int
	}{
		{"2394010", "Palworld Dedicated Server", 39612183, "25247047", 1789441330, 1789107835, 1},
		{"896660", "Valheim Dedicated Server", 39574486, "25730807", 1791288695, 1791214462, 7},
		{"4019830", "RuneScape: Dragonwilds Dedicated Server", 39619344, "25805654", 1791468210, 1791468210, 1},
		{"2857200", "Abiotic Factor Dedicated Server", 39524384, "24343458", 1785179524, 1784761143, 1},
		{"2278520", "Enshrouded Dedicated Server", 39609069, "23178631", 1778508351, 1778499607, 1},
		{"1829350", "V Rising Dedicated Server", 38905983, "25169871", 1789121063, 1788794150, 6},
		{"4129620", "Windrose Dedicated Server", 39542591, "24913903", 1787642432, 1787600449, 1},
	}
	if len(apps) != len(tests) {
		t.Errorf("want %d apps, got %d", len(tests), len(apps))
	}
	for _, tt := range tests {
		t.Run(tt.appID, func(t *testing.T) {
			a, ok := apps[tt.appID]
			if !ok {
				t.Fatalf("app %s missing", tt.appID)
			}
			if a.AppID != tt.appID || a.Name != tt.name || a.ChangeNumber != tt.change {
				t.Errorf("got id=%q name=%q change=%d", a.AppID, a.Name, a.ChangeNumber)
			}
			if a.Truncated || a.Unknown {
				t.Errorf("a complete block reads as truncated=%v unknown=%v", a.Truncated, a.Unknown)
			}
			if len(a.Branches) != tt.branches {
				t.Errorf("want %d branches, got %d: %v", tt.branches, len(a.Branches), a.Branches)
			}
			pub := a.Branches["public"]
			if pub.BuildID != tt.build || pub.TimeUpdated != tt.updated || pub.TimeBuildUpdated != tt.made {
				t.Errorf("public = %+v, want build %s updated %d made %d", pub, tt.build, tt.updated, tt.made)
			}
		})
	}
}

// Non-public branches sit beside public, and the depots above them hold
// manifests keyed by the same names. Each branch must read its own build, not
// the first buildid (or gid) the parser happens to meet.
func TestParseAppInfo_IndexesBranchesByName(t *testing.T) {
	apps, err := ParseAppInfo(appInfoFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	v := apps["896660"].Branches
	for name, want := range map[string]Branch{
		"public":         {BuildID: "25730807", TimeUpdated: 1791288695, TimeBuildUpdated: 1791214462},
		"default_old":    {BuildID: "25527701", TimeUpdated: 1790338729, TimeBuildUpdated: 1790337367, Description: "Previous stable"},
		"default_preml":  {BuildID: "20222098", TimeUpdated: 1759478369, TimeBuildUpdated: 1759406180, Description: "Last stable build before Mistlands"},
		"default_prebw":  {BuildID: "20221628", TimeUpdated: 1759477997, TimeBuildUpdated: 1759404132, Description: "Last stable build before Bog Witch"}, // the quoted value starts with a tab
		"default_pre1_0": {BuildID: "21981590", TimeUpdated: 1771576792, TimeBuildUpdated: 1771406785, Description: "Last stable build before 1.0"},
	} {
		if got := v[name]; got != want {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}
}

// The same session with Windows line endings: steamcmd.exe's output through a
// Windows container's log stream. The Agent's line scanner already drops the
// \r, but a caller handing in raw output must not have to.
func TestParseAppInfo_CRLF(t *testing.T) {
	apps, err := ParseAppInfo(strings.ReplaceAll(appInfoFixture(t), "\n", "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := apps["2394010"].Branches["public"].BuildID; got != "25247047" {
		t.Errorf("public build over CRLF = %q", got)
	}
	if got := apps["896660"].Branches["default_old"].BuildID; got != "25527701" {
		t.Errorf("default_old over CRLF = %q", got)
	}
}

func TestParseAppInfo_EdgeCases(t *testing.T) {
	const preamble = "Steam Console Client (c) Valve Corporation - version 1788292693\n" +
		"-- type 'quit' to exit --\n" +
		"Loading Steam API...\x1b[0mOK\n\x1b[0m\n" +
		"Connecting anonymously to Steam Public...\x1b[0mOK\n" +
		"\x1b[0mWaiting for client config...\x1b[0mOK\n" +
		"\x1b[0mWaiting for user info...\x1b[0mOK\n"
	const palworld = "\x1b[0mAppID : 2394010, change number : 39612183/39612183, last change : Thu Oct  8 14:44:02 2026 \n" +
		"\"2394010\"\n{\n\t\"common\"\n\t{\n\t\t\"name\"\t\t\"Palworld Dedicated Server\"\n\t}\n" +
		"\t\"depots\"\n\t{\n\t\t\"branches\"\n\t\t{\n" +
		"\t\t\t\"public\"\n\t\t\t{\n\t\t\t\t\"buildid\"\t\t\"25247047\"\n\t\t\t\t\"timeupdated\"\t\t\"1789441330\"\n\t\t\t}\n" +
		"\t\t\t\"beta\"\n\t\t\t{\n\t\t\t\t\"buildid\"\t\t\"25300000\"\n\t\t\t}\n" +
		"\t\t}\n\t}\n}\n"

	tests := []struct {
		name   string
		output string
		check  func(t *testing.T, apps map[string]AppInfo)
	}{{
		// Real output for an app id Steam has never heard of (2026-10-08).
		name:   "unknown app is an empty block",
		output: preamble + "\x1b[0mAppID : 999999999, change number : 39620996/39620996, last change : Thu Jan  1 00:00:00 1970 \n\"999999999\"\n{\n}\n" + palworld,
		check: func(t *testing.T, apps map[string]AppInfo) {
			u := apps["999999999"]
			if !u.Unknown || u.Truncated || u.Branches != nil {
				t.Errorf("unknown app = %+v", u)
			}
			if apps["2394010"].Branches["public"].BuildID != "25247047" {
				t.Errorf("the app after an empty block was lost: %+v", apps["2394010"])
			}
		},
	}, {
		// SteamCMD's fresh-home quirk: the block arrives with common but no
		// depots. Branches must be nil (ask again), not an empty map.
		name:   "no branches key",
		output: preamble + "AppID : 2394010, change number : 1/1, last change : x\n\"2394010\"\n{\n\t\"common\"\n\t{\n\t\t\"name\"\t\t\"Palworld Dedicated Server\"\n\t}\n}\n",
		check: func(t *testing.T, apps map[string]AppInfo) {
			a := apps["2394010"]
			if a.Branches != nil || a.Unknown || a.Truncated || a.Name != "Palworld Dedicated Server" {
				t.Errorf("block without branches = %+v", a)
			}
		},
	}, {
		// The session died inside the beta branch: public closed and is
		// trusted, beta did not and is dropped.
		name:   "truncated inside a branch",
		output: preamble + palworld[:strings.Index(palworld, "\"25300000\"")+4],
		check: func(t *testing.T, apps map[string]AppInfo) {
			a := apps["2394010"]
			if !a.Truncated {
				t.Error("a cut-off block must say so")
			}
			if a.Branches["public"].BuildID != "25247047" {
				t.Errorf("public closed before the cut and must survive: %+v", a.Branches)
			}
			if _, ok := a.Branches["beta"]; ok {
				t.Errorf("a half-read branch must not report a build: %+v", a.Branches["beta"])
			}
		},
	}, {
		name:   "truncated right after the header",
		output: preamble + "AppID : 2394010, change number : 1/1, last change : x\n",
		check: func(t *testing.T, apps map[string]AppInfo) {
			if a := apps["2394010"]; !a.Truncated || a.Branches != nil {
				t.Errorf("header-only block = %+v", a)
			}
		},
	}, {
		name:   "non-public branch beside public",
		output: preamble + palworld + "Unloading Steam API...\x1b[0mOK\n\x1b[0m",
		check: func(t *testing.T, apps map[string]AppInfo) {
			b := apps["2394010"].Branches
			if b["public"].BuildID != "25247047" || b["beta"].BuildID != "25300000" {
				t.Errorf("branches = %+v", b)
			}
			if b["public"].TimeBuildUpdated != 0 {
				t.Errorf("an absent timebuildupdated must read 0, got %d", b["public"].TimeBuildUpdated)
			}
		},
	}, {
		// A Windows path ending in a backslash must not swallow the line.
		name: "trailing backslash in a value",
		output: preamble + "AppID : 1, change number : 1/1, last change : x\n\"1\"\n{\n\t\"config\"\n\t{\n\t\t\"installdir\"\t\t\"bin\\\"\n\t}\n" +
			"\t\"depots\"\n\t{\n\t\t\"branches\"\n\t\t{\n\t\t\t\"public\"\n\t\t\t{\n\t\t\t\t\"buildid\"\t\t\"7\"\n\t\t\t}\n\t\t}\n\t}\n}\n",
		check: func(t *testing.T, apps map[string]AppInfo) {
			if a := apps["1"]; a.Truncated || a.Branches["public"].BuildID != "7" {
				t.Errorf("app after a trailing backslash = %+v", a)
			}
		},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apps, err := ParseAppInfo(tt.output)
			if err != nil {
				t.Fatalf("ParseAppInfo: %v", err)
			}
			tt.check(t, apps)
		})
	}
}

// A session that never printed an app block (login failed, Steam down) is an
// error, not an empty success.
func TestParseAppInfo_NoAppBlock(t *testing.T) {
	for _, out := range []string{
		"",
		"Loading Steam API...\x1b[0mOK\nConnecting anonymously to Steam Public...FAILED (No Connection)\n",
	} {
		if apps, err := ParseAppInfo(out); err == nil {
			t.Errorf("ParseAppInfo(%q) = %v, want an error", out, apps)
		}
	}
}
