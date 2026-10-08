package spec

import (
	"strings"
	"testing"
)

// The build check is derived from the install script (#392): every Steam spec
// is covered with no edit, per platform, and a script that never runs
// app_update has nothing to check.
func TestUpdateCheckFor_Derived(t *testing.T) {
	s := validSpec() // linux-native, steam_app_ids.linux 896660, app_update script
	s.SteamAppIDs = map[string]int{"linux": 896660, "windows": 896661}
	s.Platforms = append(s.Platforms,
		Platform{Kind: LinuxWine, Image: "wine", InstallScript: "steamcmd +@sSteamCmdForcePlatformType windows +app_update {{APP_ID}} validate +quit"},
		Platform{Kind: WindowsNative, Image: "win", InstallScript: "echo no steam here"},
	)

	if got, want := s.UpdateCheckFor(LinuxNative), (UpdateCheck{Method: UpdateCheckSteam, AppID: 896660, Branch: "public"}); got != want {
		t.Errorf("linux-native: got %+v, want %+v", got, want)
	}
	// Wine runs the Windows depot, so it is checked against the windows app id.
	if got, want := s.UpdateCheckFor(LinuxWine), (UpdateCheck{Method: UpdateCheckSteam, AppID: 896661, Branch: "public"}); got != want {
		t.Errorf("linux-wine: got %+v, want %+v", got, want)
	}
	// The platform's own script has no app_update, so there is no build.
	if got := s.UpdateCheckFor(WindowsNative); got.Method != UpdateCheckNone || got.Steam() {
		t.Errorf("windows-native without app_update: got %+v, want method none", got)
	}

	// app_update with no app id for the platform cannot run: derived, it is
	// not a Validate error, it just is not a Steam check.
	s.SteamAppIDs = nil
	if got := s.UpdateCheckFor(LinuxNative); got.Method != UpdateCheckSteam || got.Steam() {
		t.Errorf("no app id: got %+v, want method steam that cannot run", got)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("a derived check without an app id must not fail Validate: %v", err)
	}
}

// A declared block wins over the derivation, and fills its gaps from it.
func TestUpdateCheckFor_Declared(t *testing.T) {
	s := validSpec()
	s.Install.UpdateCheck = &UpdateCheck{Method: UpdateCheckNone}
	if got := s.UpdateCheckFor(LinuxNative); got.Method != UpdateCheckNone {
		t.Errorf("method none: got %+v", got)
	}

	s.Install.UpdateCheck = &UpdateCheck{Method: UpdateCheckSteam, Branch: "beta"}
	if got, want := s.UpdateCheckFor(LinuxNative), (UpdateCheck{Method: UpdateCheckSteam, AppID: 896660, Branch: "beta"}); got != want {
		t.Errorf("branch only: got %+v, want %+v", got, want)
	}

	// An explicit app id covers a script that does not say app_update at all.
	s.Install.Script = "./install-from-a-wrapper.sh"
	s.Install.UpdateCheck = &UpdateCheck{Method: UpdateCheckSteam, AppID: 42}
	if got, want := s.UpdateCheckFor(LinuxNative), (UpdateCheck{Method: UpdateCheckSteam, AppID: 42, Branch: "public"}); got != want {
		t.Errorf("explicit app id: got %+v, want %+v", got, want)
	}
}

func TestUpdateCheckValidate(t *testing.T) {
	ok := []*UpdateCheck{
		{Method: UpdateCheckNone},
		{Method: UpdateCheckSteam},
		{Method: UpdateCheckSteam, AppID: 7, Branch: "experimental"},
	}
	for _, uc := range ok {
		s := validSpec()
		s.Install.UpdateCheck = uc
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: want valid, got %v", uc, err)
		}
	}

	bad := map[string]func(*Spec){
		"unknown method": func(s *Spec) { s.Install.UpdateCheck = &UpdateCheck{Method: "rss"} },
		"empty method":   func(s *Spec) { s.Install.UpdateCheck = &UpdateCheck{} },
		"negative app":   func(s *Spec) { s.Install.UpdateCheck = &UpdateCheck{Method: UpdateCheckSteam, AppID: -1} },
		"branch quote":   func(s *Spec) { s.Install.UpdateCheck = &UpdateCheck{Method: UpdateCheckSteam, Branch: `pub"lic`} },
		"steam with no app id": func(s *Spec) {
			s.SteamAppIDs = nil
			s.Install.UpdateCheck = &UpdateCheck{Method: UpdateCheckSteam}
		},
		// Every platform needs an app id, not just the first.
		"one platform without an app id": func(s *Spec) {
			s.Platforms = append(s.Platforms, Platform{Kind: WindowsNative, Image: "win"})
			s.Install.UpdateCheck = &UpdateCheck{Method: UpdateCheckSteam}
		},
	}
	for name, mutate := range bad {
		s := validSpec()
		mutate(s)
		err := s.Validate()
		if err == nil || !strings.Contains(err.Error(), "update_check") {
			t.Errorf("%s: want an update_check error, got %v", name, err)
		}
	}
}
