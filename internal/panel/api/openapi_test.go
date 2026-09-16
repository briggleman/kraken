package api

import (
	"fmt"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestOpenAPISpecValid parses the embedded OpenAPI document and asserts it is
// well-formed and covers the major endpoints — a guard against the spec drifting
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

// A YAML flow mapping ends its value at the first comma, so an unquoted
// `{ description: A, B }` parses as a truncated description plus a key named
// "B" with no value — valid YAML, silently wrong document, and invisible in a
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
					t.Errorf("openapi.yaml: %s/%s has no value — an unquoted comma in a flow mapping truncated it", path, k)
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
