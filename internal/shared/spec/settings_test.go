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

func TestMissingRequiredSettings(t *testing.T) {
	s := &Spec{Settings: Settings{Groups: []SettingGroup{
		{ID: "server", Fields: []SettingField{
			{Key: "OwnerId", Label: "Owner Player ID", Type: FieldString, Required: true},
			{Key: "ServerName", Type: FieldString, Default: "Kraken"},
			{Key: "Seed", Type: FieldString, Required: true},
		}},
		{ID: "extra", Fields: []SettingField{
			// Required with a default: blanking it hands it back to the default
			// (#367), so it is never missing.
			{Key: "Region", Type: FieldString, Default: "us", Required: true},
		}},
	}}}

	keys := func(fs []SettingField) []string {
		out := make([]string, 0, len(fs))
		for _, f := range fs {
			out = append(out, f.Key)
		}
		return out
	}

	cases := []struct {
		name   string
		values map[string]string
		want   []string
	}{
		// A fresh server's effective settings are the spec defaults — this is the
		// Dragonwilds deploy: every required field without a default is missing.
		{"spec defaults", s.ResolveSettings(nil), []string{"OwnerId", "Seed"}},
		{"all set", s.ResolveSettings(map[string]string{"OwnerId": "0002a", "Seed": "42"}), nil},
		// Whitespace is not a value. An owner id of three spaces is still blank to
		// the game, and still crashes it.
		{"whitespace only", s.ResolveSettings(map[string]string{"OwnerId": "   ", "Seed": "42"}), []string{"OwnerId"}},
		{"required default blanked", s.ResolveSettings(map[string]string{
			"OwnerId": "0002a", "Seed": "42", "Region": "",
		}), nil},
		// A key absent from the map entirely — a caller that forgot to resolve —
		// reads as missing rather than as satisfied.
		{"absent from values", map[string]string{}, []string{"OwnerId", "Seed", "Region"}},
		// Non-required empties never count.
		{"optional blank", s.ResolveSettings(map[string]string{
			"OwnerId": "0002a", "Seed": "42", "ServerName": "",
		}), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := keys(s.MissingRequiredSettings(c.values))
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("missing: got %v, want %v (declared order)", got, c.want)
			}
		})
	}
}

// TestResolveSettings_RequiredBlankYieldsToDefault — #367. A server stores its
// settings in full at create, so a required field the spec gave no default is
// stored as "". When the spec later gains a default for it, that blank must not
// outrank the default: the server would otherwise stay refused by the start
// gate until someone typed the new default in by hand.
func TestResolveSettings_RequiredBlankYieldsToDefault(t *testing.T) {
	field := func(def string, required bool) *Spec {
		return &Spec{Settings: Settings{Groups: []SettingGroup{{ID: "server", Fields: []SettingField{
			{Key: "OwnerId", Label: "Owner Player ID", Type: FieldString, Default: def, Required: required},
			{Key: "ServerName", Type: FieldString, Default: "Kraken"},
		}}}}}
	}
	before := field("", true)
	stored := before.ResolveSettings(nil) // what create saves on the row
	if v, ok := stored["OwnerId"]; !ok || v != "" {
		t.Fatalf("create should store the default-less required field as a blank, got %q (present %v)", v, ok)
	}
	if n := len(before.MissingRequiredSettings(before.ResolveSettings(stored))); n != 1 {
		t.Fatalf("before the spec gains a default the field should be missing, got %d missing", n)
	}

	cases := []struct {
		name        string
		spec        *Spec
		stored      string
		wantValue   string
		wantMissing bool
		wantSpec    bool // the value is reported as coming from the spec
	}{
		{"gained default lifts the gate", field("0002a", true), "", "0002a", false, true},
		{"whitespace-only yields too", field("0002a", true), "   \t", "0002a", false, true},
		{"operator value wins", field("0002a", true), "00ffee", "00ffee", false, false},
		{"empty default stays missing", field("", true), "", "", true, false},
		{"whitespace default stays missing", field("  ", true), "", "", true, false},
		// Not required: a blank is a value an operator may have chosen, so the
		// spec's default must not overwrite it.
		{"non-required blank does not yield", field("0002a", false), "", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := map[string]string{"OwnerId": c.stored, "ServerName": "Midgard"}
			eff := c.spec.ResolveSettings(st)
			if eff["OwnerId"] != c.wantValue {
				t.Fatalf("effective OwnerId: got %q, want %q", eff["OwnerId"], c.wantValue)
			}
			if eff["ServerName"] != "Midgard" {
				t.Fatalf("an unrelated stored value changed: %q", eff["ServerName"])
			}
			if got := len(c.spec.MissingRequiredSettings(eff)) == 1; got != c.wantMissing {
				t.Fatalf("missing: got %v, want %v", got, c.wantMissing)
			}
			fromSpec := strings.Join(c.spec.SettingsFromSpec(st), ",")
			if want := map[bool]string{true: "OwnerId", false: ""}[c.wantSpec]; fromSpec != want {
				t.Fatalf("from spec: got %q, want %q", fromSpec, want)
			}
		})
	}
}

// TestSettingsFromSpec_AbsentKey — a field the spec added after the server was
// created is absent from the stored map, so its value is the spec's default and
// is reported as such. The list is never nil: it goes out as a JSON array.
func TestSettingsFromSpec_AbsentKey(t *testing.T) {
	s := &Spec{Settings: Settings{Groups: []SettingGroup{{ID: "g", Fields: []SettingField{
		{Key: "a", Type: FieldString, Default: "x"},
		{Key: "b", Type: FieldString, Default: "y"},
	}}}}}
	if got := s.SettingsFromSpec(map[string]string{"a": "mine"}); strings.Join(got, ",") != "b" {
		t.Fatalf("from spec: got %v, want [b]", got)
	}
	// A required field the spec added later with NO default is absent from
	// the row and blank in effect: missing, and nothing from the spec to show.
	added := &Spec{Settings: Settings{Groups: []SettingGroup{{ID: "g", Fields: []SettingField{
		{Key: "a", Type: FieldString, Default: "x"},
		{Key: "OwnerId", Type: FieldString, Required: true},
		{Key: "Motd", Type: FieldString},
	}}}}}
	stored := map[string]string{"a": "mine"}
	if got := added.SettingsFromSpec(stored); len(got) != 0 {
		t.Fatalf("from spec with blank defaults: got %v, want none", got)
	}
	if n := len(added.MissingRequiredSettings(added.ResolveSettings(stored))); n != 1 {
		t.Fatalf("an added required field with no default should be missing, got %d missing", n)
	}
	if got := s.SettingsFromSpec(map[string]string{"a": "1", "b": "2"}); got == nil || len(got) != 0 {
		t.Fatalf("from spec with every key stored: got %#v, want an empty non-nil slice", got)
	}
}

// TestSettingsToStore_KeepsRequiredBlank — a settings save writes the row back
// from this, and it must keep a required field's blank as a blank: freezing
// today's default into the row would stop the field following the spec, and
// would report it as the operator's own value.
func TestSettingsToStore_KeepsRequiredBlank(t *testing.T) {
	s := &Spec{Settings: Settings{Groups: []SettingGroup{{ID: "g", Fields: []SettingField{
		{Key: "OwnerId", Type: FieldString, Default: "0002a", Required: true},
		{Key: "ServerName", Type: FieldString, Default: "Kraken"},
		{Key: "Added", Type: FieldString, Default: "later"},
	}}}}}
	got := s.SettingsToStore(map[string]string{"OwnerId": "", "ServerName": "Midgard", "Gone": "x"})
	want := map[string]string{"OwnerId": "", "ServerName": "Midgard", "Added": "later"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q, want %q (all: %v)", k, got[k], v, got)
		}
	}

	// A REQUIRED field the spec added after the server was created is absent
	// from the row. Its default must not be frozen in either: it is stored
	// blank, and so keeps following the spec.
	got = s.SettingsToStore(map[string]string{"ServerName": "Midgard"})
	want = map[string]string{"OwnerId": "", "ServerName": "Midgard", "Added": "later"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestRequiredSettingParsesFromJSON(t *testing.T) {
	var f SettingField
	if err := json.Unmarshal([]byte(`{"key":"OwnerId","type":"string","required":true}`), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !f.Required {
		t.Fatal("required: true must survive decoding")
	}
	// And stays out of the encoding when false, like read_only.
	out, _ := json.Marshal(SettingField{Key: "x", Type: FieldString})
	if strings.Contains(string(out), "required") {
		t.Fatalf("a non-required field must not serialize the key: %s", out)
	}
}

func TestValidateSettings_RejectsRequiredReadOnly(t *testing.T) {
	s := validSpec()
	s.Settings = Settings{Groups: []SettingGroup{{ID: "g", Fields: []SettingField{
		{Key: "a", Type: FieldString, Required: true, ReadOnly: true},
	}}}}
	// An operator cannot change it, so it would either be satisfied forever or
	// block every start of every server built from the spec.
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "both required and read_only") {
		t.Fatalf("expected required+read_only rejection, got %v", err)
	}
}

func TestValidateSettings_RejectsRequiredBool(t *testing.T) {
	s := validSpec()
	s.Settings = Settings{Groups: []SettingGroup{{ID: "g", Fields: []SettingField{
		{Key: "PvP", Type: FieldBool, Required: true},
	}}}}
	// Off is an answer, not a blank: the Settings tab has no empty state for a
	// checkbox to mark, so the gate could never say what to fill in.
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "a bool cannot be required") {
		t.Fatalf("expected required bool rejection, got %v", err)
	}
	// The same field unrequired is fine.
	s.Settings.Groups[0].Fields[0].Required = false
	if err := s.Validate(); err != nil {
		t.Fatalf("an optional bool must validate: %v", err)
	}
}
