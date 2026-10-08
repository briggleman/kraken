package steam

import (
	"strings"
	"testing"
)

// acfPalworldLinux is the shape SteamCMD leaves in /data/steamapps after a
// Linux install of Palworld's dedicated server: tab-separated pairs, nested
// InstalledDepots/UserConfig/MountedConfig blocks, and a TargetBuildID beside
// the buildid.
const acfPalworldLinux = `"AppState"
{
	"appid"		"2394010"
	"Universe"		"1"
	"LauncherPath"		"/home/steam/steamcmd/linux32/steamcmd"
	"name"		"Palworld Dedicated Server"
	"StateFlags"		"4"
	"installdir"		"PalServer"
	"LastUpdated"		"1759921187"
	"LastPlayed"		"0"
	"SizeOnDisk"		"3519412953"
	"StagingSize"		"0"
	"buildid"		"25247047"
	"LastOwner"		"76561202255233023"
	"DownloadType"		"1"
	"UpdateResult"		"0"
	"BytesToDownload"		"1061279744"
	"BytesDownloaded"		"1061279744"
	"BytesToStage"		"3519412953"
	"BytesStaged"		"3519412953"
	"TargetBuildID"		"25247047"
	"AutoUpdateBehavior"		"0"
	"AllowOtherDownloadsWhileRunning"		"0"
	"ScheduledAutoUpdate"		"0"
	"InstalledDepots"
	{
		"1006"
		{
			"manifest"		"7138471031118904166"
			"size"		"72960342"
		}
		"2394012"
		{
			"manifest"		"4603741190199495340"
			"size"		"3446452611"
		}
	}
	"SharedDepots"
	{
		"228985"		"228980"
	}
	"UserConfig"
	{
	}
	"MountedConfig"
	{
	}
}
`

// acfDragonwildsWindows is the Windows shape: CRLF line endings and a
// LauncherPath whose backslashes are escaped.
const acfDragonwildsWindows = "\"AppState\"\r\n{\r\n" +
	"\t\"appid\"\t\t\"4019830\"\r\n" +
	"\t\"Universe\"\t\t\"1\"\r\n" +
	"\t\"LauncherPath\"\t\t\"C:\\\\steamcmd\\\\steamcmd.exe\"\r\n" +
	"\t\"name\"\t\t\"RuneScape: Dragonwilds Dedicated Server\"\r\n" +
	"\t\"StateFlags\"\t\t\"4\"\r\n" +
	"\t\"LastUpdated\"\t\t\"1759850000\"\r\n" +
	"\t\"buildid\"\t\t\"25630937\"\r\n" +
	"\t\"InstalledDepots\"\r\n\t{\r\n\t\t\"4019831\"\r\n\t\t{\r\n\t\t\t\"manifest\"\t\t\"1\"\r\n\t\t}\r\n\t}\r\n" +
	"}\r\n"

func TestParseAppManifest(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Manifest
	}{
		{"linux palworld", acfPalworldLinux,
			Manifest{AppID: "2394010", Name: "Palworld Dedicated Server", BuildID: "25247047", LastUpdated: 1759921187}},
		{"windows dragonwilds", acfDragonwildsWindows,
			Manifest{AppID: "4019830", Name: "RuneScape: Dragonwilds Dedicated Server", BuildID: "25630937", LastUpdated: 1759850000}},
		// A byte-order mark and a comment are not part of the data.
		{"bom and comment", "\xef\xbb\xbf// written by hand\n\"AppState\" { \"appid\" \"1\" \"buildid\" \"7\" }",
			Manifest{AppID: "1", BuildID: "7"}},
		// Steam reads keys without regard to case.
		{"key case", `"appstate" { "APPID" "1" "BuildID" "8" "lastupdated" "5" }`,
			Manifest{AppID: "1", BuildID: "8", LastUpdated: 5}},
		// A buildid inside a nested block is not the installed build.
		{"nested buildid ignored", `"AppState" { "UserConfig" { "buildid" "999" } "buildid" "10" }`,
			Manifest{BuildID: "10"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAppManifest([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParseAppManifest: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Anything that does not yield a build is an error, never a zero Manifest a
// caller could compare with a real build id.
func TestParseAppManifestRejects(t *testing.T) {
	cases := map[string]string{
		"empty":               "",
		"no AppState":         `"Other" { "buildid" "1" }`,
		"no buildid":          `"AppState" { "appid" "1" }`,
		"truncated":           strings.Split(acfPalworldLinux, `"InstalledDepots"`)[0],
		"unterminated string": `"AppState" { "buildid" "1`,
		"stray brace":         `"AppState" { "buildid" "1" } }`,
		"key without value":   `"AppState" { "buildid" }`,
		"bad LastUpdated":     `"AppState" { "buildid" "1" "LastUpdated" "yesterday" }`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if m, err := ParseAppManifest([]byte(in)); err == nil {
				t.Fatalf("want an error, got %+v", m)
			}
		})
	}
}
