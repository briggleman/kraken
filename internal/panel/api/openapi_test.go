package api

import (
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
