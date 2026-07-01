package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBindingUnmarshal_StringShorthand(t *testing.T) {
	var b Binding
	if err := json.Unmarshal([]byte(`"world_name"`), &b); err != nil {
		t.Fatalf("unmarshal string binding: %v", err)
	}
	if b.From != "world_name" || b.Map != nil {
		t.Fatalf("unexpected binding: %+v", b)
	}
}

func TestBindingUnmarshal_Object(t *testing.T) {
	var b Binding
	if err := json.Unmarshal([]byte(`{"from":"pvp","map":{"true":"1","false":"0"}}`), &b); err != nil {
		t.Fatalf("unmarshal object binding: %v", err)
	}
	if b.From != "pvp" || b.Map["true"] != "1" {
		t.Fatalf("unexpected binding: %+v", b)
	}
}

func TestRenderConfig_Adapters(t *testing.T) {
	settings := map[string]string{"world_name": "Midgard", "max_players": "16", "pvp": "true"}

	tests := []struct {
		name   string
		cf     ConfigFile
		expect string
	}{
		{
			name: "source-cvar",
			cf: ConfigFile{Format: FormatSourceCvar, Bindings: map[string]Binding{
				"servername": {From: "world_name"},
				"maxplayers": {From: "max_players"},
			}},
			expect: "maxplayers \"16\"\nservername \"Midgard\"\n", // keys sorted
		},
		{
			name: "properties",
			cf: ConfigFile{Format: FormatProperties, Bindings: map[string]Binding{
				"level-name": {From: "world_name"},
			}},
			expect: "level-name=Midgard\n",
		},
		{
			name: "env uppercases keys",
			cf: ConfigFile{Format: FormatEnv, Bindings: map[string]Binding{
				"world": {From: "world_name"},
			}},
			expect: "WORLD=Midgard\n",
		},
		{
			name: "ini with section",
			cf: ConfigFile{Format: FormatINI, Section: "Server", Bindings: map[string]Binding{
				"Name": {From: "world_name"},
			}},
			expect: "[Server]\nName=Midgard\n",
		},
		{
			name: "value remap",
			cf: ConfigFile{Format: FormatSourceCvar, Bindings: map[string]Binding{
				"sv_pvp": {From: "pvp", Map: map[string]string{"true": "1", "false": "0"}},
			}},
			expect: "sv_pvp \"1\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderConfig(tt.cf, RenderContext{Settings: settings})
			if err != nil {
				t.Fatalf("RenderConfig: %v", err)
			}
			if got != tt.expect {
				t.Fatalf("got %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestRenderConfig_Template(t *testing.T) {
	cf := ConfigFile{
		Format:   FormatTemplate,
		Template: "name={{ .settings.world_name }} port={{ .ports.game }} pvp={{ if eq .settings.pvp \"true\" }}1{{ else }}0{{ end }}",
	}
	got, err := RenderConfig(cf, RenderContext{
		Settings: map[string]string{"world_name": "Midgard", "pvp": "true"},
		Ports:    map[string]int{"game": 2456},
	})
	if err != nil {
		t.Fatalf("RenderConfig template: %v", err)
	}
	if got != "name=Midgard port=2456 pvp=1" {
		t.Fatalf("unexpected template output: %q", got)
	}
}

func TestResolveSettings(t *testing.T) {
	s := &Spec{Settings: Settings{Groups: []SettingGroup{{
		ID: "world", Fields: []SettingField{
			{Key: "world_name", Type: FieldString, Default: "Midgard"},
			{Key: "max_players", Type: FieldInt, Default: "16"},
		},
	}}}}
	vals := s.ResolveSettings(map[string]string{"max_players": "32"})
	if vals["world_name"] != "Midgard" || vals["max_players"] != "32" {
		t.Fatalf("unexpected resolved settings: %+v", vals)
	}
}

func TestValidateFieldValue(t *testing.T) {
	min := 1.0
	max := 64.0
	intField := SettingField{Key: "max_players", Type: FieldInt, Min: &min, Max: &max}
	if err := ValidateFieldValue(intField, "16"); err != nil {
		t.Fatalf("16 should be valid: %v", err)
	}
	if err := ValidateFieldValue(intField, "100"); err == nil {
		t.Fatal("100 should exceed max")
	}
	if err := ValidateFieldValue(intField, "abc"); err == nil {
		t.Fatal("abc should not be an int")
	}
	enumField := SettingField{Key: "difficulty", Type: FieldEnum, Options: []string{"easy", "hard"}}
	if err := ValidateFieldValue(enumField, "hard"); err != nil {
		t.Fatalf("hard should be valid: %v", err)
	}
	if err := ValidateFieldValue(enumField, "insane"); err == nil {
		t.Fatal("insane should not be allowed")
	}
}

func TestValidateSettings_RejectsUnknownBinding(t *testing.T) {
	s := validSpec()
	s.Settings = Settings{Groups: []SettingGroup{{ID: "g", Fields: []SettingField{{Key: "a", Type: FieldString}}}}}
	s.ConfigFiles = []ConfigFile{{Path: "/data/x.cfg", Format: FormatSourceCvar, Bindings: map[string]Binding{
		"k": {From: "does_not_exist"},
	}}}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "unknown setting") {
		t.Fatalf("expected unknown-setting error, got %v", err)
	}
}
