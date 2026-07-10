package spec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"
)

// FieldType is the input type of a game setting, driving UI rendering + validation.
type FieldType string

const (
	FieldString   FieldType = "string"
	FieldText     FieldType = "text"
	FieldInt      FieldType = "int"
	FieldFloat    FieldType = "float"
	FieldBool     FieldType = "bool"
	FieldEnum     FieldType = "enum"
	FieldPassword FieldType = "password"
)

func (t FieldType) valid() bool {
	switch t {
	case FieldString, FieldText, FieldInt, FieldFloat, FieldBool, FieldEnum, FieldPassword:
		return true
	default:
		return false
	}
}

// SettingField is one editable game setting.
type SettingField struct {
	Key     string    `json:"key"`
	Label   string    `json:"label,omitempty"`
	Type    FieldType `json:"type"`
	Default string    `json:"default,omitempty"`
	Help    string    `json:"help,omitempty"`
	Options []string  `json:"options,omitempty"` // enum
	Min     *float64  `json:"min,omitempty"`
	Max     *float64  `json:"max,omitempty"`
	Pattern string    `json:"pattern,omitempty"`
	// ReadOnly marks a field as display-only: the UI renders it disabled and the
	// Panel rejects attempts to change its value via the settings API.
	ReadOnly bool `json:"read_only,omitempty"`
}

// SettingGroup is a labeled cluster of fields (a UI section/tab).
type SettingGroup struct {
	ID          string         `json:"id"`
	Label       string         `json:"label,omitempty"`
	Description string         `json:"description,omitempty"`
	Fields      []SettingField `json:"fields"`
}

// Settings is the game's editable configuration surface.
type Settings struct {
	Groups []SettingGroup `json:"groups,omitempty"`
	// HotReload declares that the game re-reads its config files while running,
	// so saved settings apply live without a restart. It only changes what the
	// UI tells the operator after a save — config files are pushed to the node
	// either way. Launch variables are never hot-reloadable (they're baked into
	// the start command at boot).
	HotReload bool `json:"hot_reload,omitempty"`
}

// fields returns every field across all groups.
func (s Settings) fields() []SettingField {
	var out []SettingField
	for _, g := range s.Groups {
		out = append(out, g.Fields...)
	}
	return out
}

// ConfigFormat selects how a ConfigFile emits values.
type ConfigFormat string

const (
	FormatTemplate   ConfigFormat = "template"
	FormatSourceCvar ConfigFormat = "source-cvar" // key "value"
	FormatINI        ConfigFormat = "ini"         // [section]\nkey=value
	FormatProperties ConfigFormat = "properties"  // key=value
	FormatKeyValue   ConfigFormat = "keyvalue"    // key=value (alias of properties)
	FormatJSON       ConfigFormat = "json"        // {"key": "value"}
	FormatEnv        ConfigFormat = "env"         // KEY=value
)

func (f ConfigFormat) valid() bool {
	switch f {
	case FormatTemplate, FormatSourceCvar, FormatINI, FormatProperties, FormatKeyValue, FormatJSON, FormatEnv:
		return true
	default:
		return false
	}
}

// Binding maps an output key to the setting it draws from, with optional value
// remapping. It accepts a JSON shorthand: a bare string is the setting key.
//
//	bindings:
//	  servername: world_name                 # shorthand → {from: world_name}
//	  pvp: { from: pvp, map: {"true":"1"} }   # explicit
type Binding struct {
	From string            `json:"from"`
	Map  map[string]string `json:"map,omitempty"`
}

// UnmarshalJSON accepts either a string (setting key) or an object.
func (b *Binding) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		b.From = s
		return nil
	}
	type alias Binding
	var a alias
	if err := json.Unmarshal(trimmed, &a); err != nil {
		return err
	}
	*b = Binding(a)
	return nil
}

// RenderContext is the data available to config rendering: resolved settings
// values, launch variables, and allocated host ports (by spec port name).
type RenderContext struct {
	Settings map[string]string
	Vars     map[string]string
	Ports    map[string]int
}

// ResolveSettings merges each field's default with user overrides and returns the
// effective settings values (string-encoded).
func (s *Spec) ResolveSettings(overrides map[string]string) map[string]string {
	out := make(map[string]string)
	for _, f := range s.Settings.fields() {
		val := f.Default
		if ov, ok := overrides[f.Key]; ok {
			val = ov
		}
		out[f.Key] = val
	}
	return out
}

// RenderConfig renders a single ConfigFile from the context, returning the file
// contents to write to ConfigFile.Path.
func RenderConfig(cf ConfigFile, ctx RenderContext) (string, error) {
	if cf.Format == FormatTemplate {
		return renderTemplate(cf.Template, ctx)
	}
	// Adapter formats resolve bindings → ordered (key, value) pairs.
	pairs := resolveBindings(cf.Bindings, ctx.Settings)
	switch cf.Format {
	case FormatSourceCvar:
		var b strings.Builder
		for _, p := range pairs {
			fmt.Fprintf(&b, "%s %q\n", p.key, p.val)
		}
		return b.String(), nil
	case FormatProperties, FormatKeyValue:
		var b strings.Builder
		for _, p := range pairs {
			fmt.Fprintf(&b, "%s=%s\n", p.key, p.val)
		}
		return b.String(), nil
	case FormatEnv:
		var b strings.Builder
		for _, p := range pairs {
			fmt.Fprintf(&b, "%s=%s\n", strings.ToUpper(p.key), p.val)
		}
		return b.String(), nil
	case FormatINI:
		var b strings.Builder
		if cf.Section != "" {
			fmt.Fprintf(&b, "[%s]\n", cf.Section)
		}
		for _, p := range pairs {
			fmt.Fprintf(&b, "%s=%s\n", p.key, p.val)
		}
		return b.String(), nil
	case FormatJSON:
		m := make(map[string]string, len(pairs))
		for _, p := range pairs {
			m[p.key] = p.val
		}
		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return "", err
		}
		return string(out) + "\n", nil
	default:
		return "", fmt.Errorf("spec: unknown config format %q", cf.Format)
	}
}

type kv struct{ key, val string }

func resolveBindings(bindings map[string]Binding, settings map[string]string) []kv {
	keys := make([]string, 0, len(bindings))
	for k := range bindings {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic output
	pairs := make([]kv, 0, len(keys))
	for _, outKey := range keys {
		bind := bindings[outKey]
		val := settings[bind.From]
		if bind.Map != nil {
			if mapped, ok := bind.Map[val]; ok {
				val = mapped
			}
		}
		pairs = append(pairs, kv{key: outKey, val: val})
	}
	return pairs
}

func renderTemplate(body string, ctx RenderContext) (string, error) {
	tmpl, err := template.New("config").Option("missingkey=zero").Parse(body)
	if err != nil {
		return "", fmt.Errorf("spec: parse config template: %w", err)
	}
	var b bytes.Buffer
	data := map[string]any{"settings": ctx.Settings, "vars": ctx.Vars, "ports": ctx.Ports}
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("spec: execute config template: %w", err)
	}
	return b.String(), nil
}

// validateSettings checks the settings schema and config files for consistency.
func (s *Spec) validateSettings() error {
	keys := make(map[string]bool)
	for gi, g := range s.Settings.Groups {
		if g.ID == "" {
			return fmt.Errorf("spec %q: settings.group[%d]: id is required", s.Slug, gi)
		}
		for fi, f := range g.Fields {
			if f.Key == "" {
				return fmt.Errorf("spec %q: group %q field[%d]: key is required", s.Slug, g.ID, fi)
			}
			if keys[f.Key] {
				return fmt.Errorf("spec %q: duplicate setting key %q", s.Slug, f.Key)
			}
			keys[f.Key] = true
			if !f.Type.valid() {
				return fmt.Errorf("spec %q: setting %q: unknown type %q", s.Slug, f.Key, f.Type)
			}
			if f.Type == FieldEnum && len(f.Options) == 0 {
				return fmt.Errorf("spec %q: enum setting %q: options are required", s.Slug, f.Key)
			}
			if f.Default != "" {
				if err := ValidateFieldValue(f, f.Default); err != nil {
					return fmt.Errorf("spec %q: setting %q: invalid default: %w", s.Slug, f.Key, err)
				}
			}
		}
	}
	for ci, cf := range s.ConfigFiles {
		if cf.Path == "" {
			return fmt.Errorf("spec %q: config_files[%d]: path is required", s.Slug, ci)
		}
		if !cf.Format.valid() {
			return fmt.Errorf("spec %q: config_files[%d]: unknown format %q", s.Slug, ci, cf.Format)
		}
		if cf.Format == FormatTemplate && cf.Template == "" {
			return fmt.Errorf("spec %q: config_files[%d]: template body is required for format=template", s.Slug, ci)
		}
		// Bindings must reference defined settings.
		for outKey, bind := range cf.Bindings {
			if bind.From == "" {
				return fmt.Errorf("spec %q: config_files[%d]: binding %q has no source", s.Slug, ci, outKey)
			}
			if !keys[bind.From] {
				return fmt.Errorf("spec %q: config_files[%d]: binding %q references unknown setting %q", s.Slug, ci, outKey, bind.From)
			}
		}
	}
	return nil
}

// ValidateFieldValue validates a single value string against a field's type and
// constraints. Used for spec defaults and user-supplied setting overrides.
func ValidateFieldValue(f SettingField, value string) error {
	switch f.Type {
	case FieldInt:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%q is not an integer", value)
		}
		if f.Min != nil && float64(n) < *f.Min {
			return fmt.Errorf("%d is below minimum %v", n, *f.Min)
		}
		if f.Max != nil && float64(n) > *f.Max {
			return fmt.Errorf("%d is above maximum %v", n, *f.Max)
		}
	case FieldFloat:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("%q is not a number", value)
		}
		if f.Min != nil && n < *f.Min {
			return fmt.Errorf("%v is below minimum %v", n, *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return fmt.Errorf("%v is above maximum %v", n, *f.Max)
		}
	case FieldBool:
		if value != "true" && value != "false" {
			return fmt.Errorf("%q is not a boolean (true/false)", value)
		}
	case FieldEnum:
		for _, opt := range f.Options {
			if value == opt {
				return nil
			}
		}
		return fmt.Errorf("%q is not one of the allowed options", value)
	}
	return nil
}
