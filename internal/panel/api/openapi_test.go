package api

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/briggleman/kraken/internal/panel/store"
)

// TestOpenAPISpecValid parses the embedded OpenAPI document and asserts it is
// well-formed and covers the major endpoints â€” a guard against the spec drifting
// into invalid YAML or losing routes.
func TestOpenAPISpecValid(t *testing.T) {
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Info    map[string]any `json:"info"`
		Paths   map[string]any `json:"paths"`
		Comp    map[string]any `json:"components"`
	}
	if err := yaml.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	if doc.OpenAPI == "" {
		t.Fatal("openapi version missing")
	}
	if len(doc.Paths) == 0 {
		t.Fatal("no paths defined")
	}
	want := []string{
		"/auth/login", "/servers", "/servers/{id}", "/servers/{id}/power",
		"/servers/{id}/schedules", "/specs", "/nodes", "/agents/enroll",
		"/agents/bootstrap-tokens", "/audit",
	}
	for _, p := range want {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("openapi.yaml missing path %q", p)
		}
	}
}

// The audit schema is what a client codes against, so a field the Panel now
// writes has to be declared. forwarded_for is the one an operator behind a NAT
// reads the real client address out of, and a client that does not know about
// it will not show it.
func TestOpenAPIAuditEntryDeclaresTheForwardedChain(t *testing.T) {
	var doc struct {
		Comp struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := yaml.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	entry, ok := doc.Comp.Schemas["AuditEntry"]
	if !ok {
		t.Fatal("openapi.yaml has no AuditEntry schema")
	}
	for _, f := range []string{"ip", "forwarded_for"} {
		if _, ok := entry.Properties[f]; !ok {
			t.Errorf("AuditEntry does not declare %q", f)
		}
	}
}

// A YAML flow mapping ends its value at the first comma, so an unquoted
// `{ description: A, B }` parses as a truncated description plus a key named
// "B" with no value â€” valid YAML, silently wrong document, and invisible in a
// rendered reference until someone reads the missing half. Nothing in the
// document legitimately has a null value, so that is the thing to assert.
func TestOpenAPIHasNoTruncatedFlowValues(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	var walk func(any, string)
	walk = func(n any, path string) {
		switch v := n.(type) {
		case map[string]any:
			for k, val := range v {
				if val == nil {
					t.Errorf("openapi.yaml: %s/%s has no value â€” an unquoted comma in a flow mapping truncated it", path, k)
					continue
				}
				walk(val, path+"/"+k)
			}
		case []any:
			for i, val := range v {
				walk(val, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walk(doc, "")
}

// TestOpenAPIScheduleActionEnumMatchesStore keeps the documented schedule
// actions and the ones the store actually accepts in lockstep: a client
// generated from the spec must be able to create every action the Panel
// honours, and must never be offered one the Panel would reject.
func TestOpenAPIScheduleActionEnumMatchesStore(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas struct {
				ScheduleInput struct {
					Properties struct {
						Action struct {
							Enum []string `json:"enum"`
						} `json:"action"`
					} `json:"properties"`
				} `json:"ScheduleInput"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := yaml.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	got := doc.Components.Schemas.ScheduleInput.Properties.Action.Enum
	if len(got) == 0 {
		t.Fatal("openapi.yaml: ScheduleInput.action has no enum")
	}
	want := make([]string, 0, len(store.ScheduleActions()))
	for _, a := range store.ScheduleActions() {
		want = append(want, string(a))
	}
	if !slices.Equal(got, want) {
		t.Errorf("ScheduleInput.action enum = %v, want store.ScheduleActions() = %v", got, want)
	}
}

// TestValidateScheduleAcceptsEveryAction asserts the handler accepts the store's
// full action set and that its 400 message names all of it â€” the drift that left
// `replicate` both undocumented and unnamed in the rejection.
func TestValidateScheduleAcceptsEveryAction(t *testing.T) {
	for _, a := range store.ScheduleActions() {
		req := scheduleRequest{Name: "nightly", Action: string(a), Cron: "0 4 * * *"}
		if a == store.ScheduleCommand {
			req.Command = "say hello"
		}
		got, _, _, err := validateSchedule(req)
		if err != nil {
			t.Errorf("validateSchedule(action=%q) returned %v, want accepted", a, err)
			continue
		}
		if got != a {
			t.Errorf("validateSchedule(action=%q) resolved to %q", a, got)
		}
	}

	_, _, _, err := validateSchedule(scheduleRequest{Action: "nonsense", Cron: "0 4 * * *"})
	if err == nil {
		t.Fatal("validateSchedule accepted an unknown action")
	}
	for _, a := range store.ScheduleActions() {
		if !strings.Contains(err.Error(), string(a)) {
			t.Errorf("validation error %q does not name the %q action", err, a)
		}
	}
}

// The console reads the audit log's retention window off the list response
// rather than hard-coding a number, which only works while the document keeps
// describing it â€” the drift between copy and behaviour is what #332 was.
func TestOpenAPIAuditListDeclaresRetentionDays(t *testing.T) {
	var doc struct {
		Paths map[string]struct {
			Get struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema struct {
							Properties map[string]struct {
								Type string `json:"type"`
							} `json:"properties"`
						} `json:"schema"`
					} `json:"content"`
				} `json:"responses"`
			} `json:"get"`
		} `json:"paths"`
	}
	if err := yaml.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	props := doc.Paths["/audit"].Get.Responses["200"].Content["application/json"].Schema.Properties
	for _, name := range []string{"entries", "retention_days"} {
		if _, ok := props[name]; !ok {
			t.Errorf("GET /audit 200 schema is missing %q", name)
		}
	}
	if got := props["retention_days"].Type; got != "integer" {
		t.Errorf("retention_days is typed %q, want integer", got)
	}
}

// A client switches on the server state, so a state the Panel writes and the
// document does not name is one the client renders as garbage. `restoring`
// (#361) is the reason this exists; `install_failed` had already drifted out.
func TestOpenAPIServerStateEnumMatchesStore(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas struct {
				ServerState struct {
					Enum []string `json:"enum"`
				} `json:"ServerState"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := yaml.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	want := make([]string, 0, len(store.ServerStates()))
	for _, st := range store.ServerStates() {
		want = append(want, string(st))
	}
	if got := doc.Components.Schemas.ServerState.Enum; !slices.Equal(got, want) {
		t.Errorf("ServerState enum = %v, want store.ServerStates() = %v", got, want)
	}
}
