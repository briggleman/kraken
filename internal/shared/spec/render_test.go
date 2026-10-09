package spec

import (
	"strings"
	"testing"
)

// TestRenderInstall_Validate covers the {{VALIDATE}} pseudo-variable (#392):
// "validate" when the pass asks for it, nothing when it does not, and always
// set, so the literal placeholder never reaches SteamCMD.
func TestRenderInstall_Validate(t *testing.T) {
	script := "steamcmd +app_update {{APP_ID}} {{VALIDATE}} +quit"
	vars := map[string]string{"APP_ID": "896660"}

	if got, want := RenderInstall(script, vars, true), "steamcmd +app_update 896660 validate +quit"; got != want {
		t.Errorf("validate pass: got %q, want %q", got, want)
	}
	if got, want := RenderInstall(script, vars, false), "steamcmd +app_update 896660  +quit"; got != want {
		t.Errorf("update pass: got %q, want %q", got, want)
	}
	if got := RenderInstall(script, nil, false); strings.Contains(got, "{{VALIDATE}}") {
		t.Errorf("nil vars: the placeholder must still render: %q", got)
	}
	if _, ok := vars[ValidateVar]; ok {
		t.Errorf("RenderInstall wrote into the caller's vars: %v", vars)
	}

	// A VALIDATE key in vars cannot shadow the pass's choice.
	shadow := map[string]string{"APP_ID": "896660", ValidateVar: "-beta x"}
	if got := RenderInstall(script, shadow, false); strings.Contains(got, "beta") {
		t.Errorf("a VALIDATE var shadowed the pseudo-variable: %q", got)
	}

	// No placeholder: exactly what Render gives, whatever the pass.
	plain := "steamcmd +app_update {{APP_ID}} validate +quit"
	for _, v := range []bool{true, false} {
		if got, want := RenderInstall(plain, vars, v), Render(plain, vars); got != want {
			t.Errorf("no placeholder (validate=%v): got %q, want %q", v, got, want)
		}
	}
}

// TestValidateVarOverrides_ReservedValidate — VALIDATE is refused as an
// override even though no spec can declare it, so the refusal names the reason
// rather than the override silently doing nothing.
func TestValidateVarOverrides_ReservedValidate(t *testing.T) {
	sp := &Spec{Variables: []Variable{{Key: "NAME", Default: "x", UserEditable: true}}}
	for _, v := range []string{"", "validate"} {
		err := sp.ValidateVarOverrides(map[string]string{ValidateVar: v})
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("override VALIDATE=%q: got %v, want a reserved-name error", v, err)
		}
	}
}
