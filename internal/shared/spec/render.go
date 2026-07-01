package spec

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var placeholderRE = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// shellUnsafe lists characters that enable shell command injection or argument
// breakout when a value is interpolated into the install/startup command, which
// the Agent runs via `/bin/sh -c`. Variable values are launch options (map
// names, player counts, server names, etc.) and never legitimately need these.
const shellUnsafe = "`$;&|<>(){}[]!*?~\"'\\"

// ValidateVarOverrides validates user-supplied variable overrides for a spec.
// Overrides for non-editable (or unknown) variables are ignored by ResolveVars,
// so only editable keys are checked. A value is rejected if it contains a shell
// metacharacter or control character — because resolved variable values are
// substituted, unescaped, into the shell command line that launches the server.
// This is the control that the per-variable Rules were meant to provide.
func (s *Spec) ValidateVarOverrides(overrides map[string]string) error {
	if len(overrides) == 0 {
		return nil
	}
	editable := make(map[string]Variable, len(s.Variables))
	for _, v := range s.Variables {
		if v.UserEditable {
			editable[v.Key] = v
		}
	}
	for key, val := range overrides {
		v, ok := editable[key]
		if !ok {
			continue
		}
		if i := strings.IndexAny(val, shellUnsafe); i >= 0 {
			return fmt.Errorf("variable %q: character %q is not allowed in a launch option", key, val[i])
		}
		for _, r := range val {
			if r < 0x20 || r == 0x7f { // control characters, incl. newline/tab
				return fmt.Errorf("variable %q: control characters are not allowed", key)
			}
		}
		if err := validateRules(v.Rules, val); err != nil {
			return fmt.Errorf("variable %q: %w", key, err)
		}
	}
	return nil
}

// validateRules enforces a variable's Rules mini-DSL — pipe-separated tokens like
// "int|min:1|max:64", "float|min:0.1", "bool", or "in:easy,normal,hard". Unknown
// tokens are ignored so the DSL can grow without breaking existing specs.
func validateRules(rules, value string) error {
	if strings.TrimSpace(rules) == "" {
		return nil
	}
	for _, raw := range strings.Split(rules, "|") {
		tok := strings.TrimSpace(raw)
		switch {
		case tok == "", tok == "string", tok == "text", tok == "password":
			// no constraint
		case tok == "int":
			if _, err := strconv.Atoi(value); err != nil {
				return fmt.Errorf("%q must be an integer", value)
			}
		case tok == "float" || tok == "number":
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return fmt.Errorf("%q must be a number", value)
			}
		case tok == "bool":
			if value != "true" && value != "false" {
				return fmt.Errorf("%q must be true or false", value)
			}
		case strings.HasPrefix(tok, "min:"):
			if err := compareBound(value, tok[len("min:"):], true); err != nil {
				return err
			}
		case strings.HasPrefix(tok, "max:"):
			if err := compareBound(value, tok[len("max:"):], false); err != nil {
				return err
			}
		case strings.HasPrefix(tok, "in:"), strings.HasPrefix(tok, "enum:"):
			list := tok[strings.IndexByte(tok, ':')+1:]
			for _, opt := range strings.Split(list, ",") {
				if strings.TrimSpace(opt) == value {
					return nil // membership satisfied; ignore remaining tokens
				}
			}
			return fmt.Errorf("%q must be one of: %s", value, list)
		}
	}
	return nil
}

// compareBound checks a numeric value against a min (isMin=true) or max bound.
func compareBound(value, bound string, isMin bool) error {
	b, err := strconv.ParseFloat(bound, 64)
	if err != nil {
		return nil // malformed bound in the spec — not the user's fault, skip
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("%q must be numeric", value)
	}
	if isMin && v < b {
		return fmt.Errorf("%v is below minimum %v", v, b)
	}
	if !isMin && v > b {
		return fmt.Errorf("%v is above maximum %v", v, b)
	}
	return nil
}

// Render substitutes {{KEY}} placeholders in s with values from vars. Unknown
// placeholders are left untouched so missing values are visible rather than
// silently blanked.
func Render(s string, vars map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(s, func(match string) string {
		key := placeholderRE.FindStringSubmatch(match)[1]
		if v, ok := vars[key]; ok {
			return v
		}
		return match
	})
}

// ResolveVars builds the substitution map for a server: it starts from each
// variable's default, applies user overrides (only for user-editable variables),
// and returns the merged map. Non-editable variables always use their default.
func (s *Spec) ResolveVars(overrides map[string]string) map[string]string {
	out := make(map[string]string, len(s.Variables))
	for _, v := range s.Variables {
		val := v.Default
		if v.UserEditable {
			if ov, ok := overrides[v.Key]; ok {
				val = ov
			}
		}
		out[v.Key] = val
	}
	return out
}

// AppIDFor returns the SteamCMD app id for the given OS family ("linux" or
// "windows") as a string, or "" if not defined.
func (s *Spec) AppIDFor(osFamily string) string {
	if s.SteamAppIDs == nil {
		return ""
	}
	if id, ok := s.SteamAppIDs[osFamily]; ok {
		return strconv.Itoa(id)
	}
	return ""
}
