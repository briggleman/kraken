package spec

import "testing"

// validSpec returns a minimal Spec that passes Validate, used as a baseline that
// individual test cases mutate to exercise one failure at a time.
func validSpec() *Spec {
	return &Spec{
		Name:        "Valheim",
		Slug:        "valheim",
		SteamAppIDs: map[string]int{"linux": 896660},
		Platforms: []Platform{
			{Kind: LinuxNative, Image: "registry/kraken/steam-base:latest"},
		},
		Install: Install{Script: "steamcmd +login anonymous +app_update {{APP_ID}} +quit"},
		Startup: Startup{
			Command: "./valheim_server -name {{NAME}}",
			Stop:    Stop{Type: StopSignal, Value: "SIGINT"},
		},
		Variables: []Variable{
			{Key: "NAME", Default: "My Server", UserEditable: true},
		},
		Ports: []Port{
			{Name: "game", Protocol: UDP, Default: 2456, Required: true},
		},
		Resources: Resources{MinMemoryMB: 2048},
	}
}

func TestSpecValidate_OK(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("expected valid spec, got error: %v", err)
	}
}

func TestSpecValidate_Failures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Spec)
	}{
		{"missing name", func(s *Spec) { s.Name = "" }},
		{"missing slug", func(s *Spec) { s.Slug = "" }},
		{"no platforms", func(s *Spec) { s.Platforms = nil }},
		{"unknown platform kind", func(s *Spec) { s.Platforms[0].Kind = "bsd-native" }},
		{"duplicate platform kind", func(s *Spec) {
			s.Platforms = append(s.Platforms, Platform{Kind: LinuxNative, Image: "x"})
		}},
		{"missing platform image", func(s *Spec) { s.Platforms[0].Image = "" }},
		{"missing install script", func(s *Spec) { s.Install.Script = "" }},
		{"missing startup command", func(s *Spec) { s.Startup.Command = "" }},
		{"bad stop type", func(s *Spec) { s.Startup.Stop.Type = "explode" }},
		{"missing stop value", func(s *Spec) { s.Startup.Stop.Value = "" }},
		{"no ports", func(s *Spec) { s.Ports = nil }},
		{"duplicate port name", func(s *Spec) {
			s.Ports = append(s.Ports, Port{Name: "game", Protocol: TCP, Default: 2457})
		}},
		{"bad port protocol", func(s *Spec) { s.Ports[0].Protocol = "icmp" }},
		{"port out of range", func(s *Spec) { s.Ports[0].Default = 70000 }},
		{"duplicate variable key", func(s *Spec) {
			s.Variables = append(s.Variables, Variable{Key: "NAME", Default: "x"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSpec()
			tt.mutate(s)
			if err := s.Validate(); err == nil {
				t.Fatalf("expected validation error for %q, got nil", tt.name)
			}
		})
	}
}

func TestImageFor(t *testing.T) {
	s := validSpec()
	img, ok := s.ImageFor(LinuxNative)
	if !ok || img != "registry/kraken/steam-base:latest" {
		t.Fatalf("ImageFor(LinuxNative) = %q, %v; want the linux image", img, ok)
	}
	if _, ok := s.ImageFor(WindowsNative); ok {
		t.Fatal("ImageFor(WindowsNative) should not be found")
	}
}

func TestPerPlatformOverrides(t *testing.T) {
	s := validSpec()
	s.Platforms = append(s.Platforms, Platform{
		Kind:           LinuxWine,
		Image:          "registry/kraken/steam-wine:latest",
		InstallScript:  "steamcmd +@sSteamCmdForcePlatformType windows +app_update {{APP_ID}} +quit",
		StartupCommand: "wine-headless /data/Server.exe -port {{PORT_GAME}}",
	})

	// The overriding platform gets its own commands …
	if got := s.InstallScriptFor(LinuxWine); got != s.Platforms[1].InstallScript {
		t.Fatalf("InstallScriptFor(LinuxWine) = %q; want the override", got)
	}
	if got := s.StartupCommandFor(LinuxWine); got != s.Platforms[1].StartupCommand {
		t.Fatalf("StartupCommandFor(LinuxWine) = %q; want the override", got)
	}
	// … while platforms without overrides fall back to the spec-level commands.
	if got := s.InstallScriptFor(LinuxNative); got != s.Install.Script {
		t.Fatalf("InstallScriptFor(LinuxNative) = %q; want spec-level script", got)
	}
	if got := s.StartupCommandFor(LinuxNative); got != s.Startup.Command {
		t.Fatalf("StartupCommandFor(LinuxNative) = %q; want spec-level command", got)
	}
	// An unknown kind also falls back rather than failing.
	if got := s.StartupCommandFor(WindowsNative); got != s.Startup.Command {
		t.Fatalf("StartupCommandFor(WindowsNative) = %q; want spec-level command", got)
	}
	// Overrides must not break validation.
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate with overrides: %v", err)
	}
}
