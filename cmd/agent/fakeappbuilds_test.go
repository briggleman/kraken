package main

import (
	"testing"

	"github.com/briggleman/kraken/internal/agent"
)

// KRAKEN_FAKE_APP_BUILDS feeds the fake-live Agent's build table (#392):
// app=build pairs, an @branch suffix for a non-public branch, and a typo that
// rejects the whole value rather than quietly dropping an app.
func TestParseFakeAppBuilds(t *testing.T) {
	got, err := parseFakeAppBuilds(" 2394010=25247047, 896660@public=25730807 ,4019830@beta=9 ")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]agent.FakeAppBuild{
		"2394010":      {BuildID: "25247047"},
		"896660":       {BuildID: "25730807"},
		"4019830@beta": {BuildID: "9"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %+v, want %+v", k, got[k], v)
		}
	}
	for _, bad := range []string{"", ",", "2394010", "2394010=", "=5", "1@=5"} {
		if m, err := parseFakeAppBuilds(bad); err == nil {
			t.Errorf("%q: want an error, got %v", bad, m)
		}
	}
}
