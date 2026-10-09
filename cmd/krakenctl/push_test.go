package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/push"
)

// The stub is the far end of the Panel's relay client in the push drill, so
// it is tested against that client rather than against hand-built requests.
func TestRelayStubOpensWhatThePanelSends(t *testing.T) {
	priv, pub, err := push.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	stub := &relayStub{key: priv, out: &out, relayToken: "relay-secret", times: -1}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	env, err := push.Seal(pub, push.Payload{
		Class: push.ClassAttend, Event: push.EventServerCrashed, ServerID: "s1", NodeID: "n1",
		Title: "dragonwilds-01", Body: "stopped unexpectedly", Thread: "server:s1", TSms: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := push.Delivery{Token: "abcd", Environment: push.EnvironmentSandbox, Envelope: env, CreatedAt: time.Now()}
	send := func(base, token string) push.Result {
		c, err := push.NewClient(base, "panel-1", token, push.WithSleep(func(context.Context, time.Duration) error { return nil }))
		if err != nil {
			t.Fatal(err)
		}
		return c.Send(context.Background(), d)
	}

	if res := send(srv.URL, "relay-secret"); res.Outcome != push.Sent {
		t.Fatalf("got %+v, want Sent", res)
	}
	stub.mu.Lock()
	line := out.String()
	stub.mu.Unlock()
	if !strings.Contains(line, "200  attend  server_crashed  dragonwilds-01 — stopped unexpectedly") {
		t.Fatalf("the stub printed %q", line)
	}

	if res := send(srv.URL, ""); res.Outcome != push.Rejected || res.Status != http.StatusUnauthorized {
		t.Fatalf("without the relay token: %+v, want a 401 refusal", res)
	}
	if res := send(srv.URL+"?answer=410", "relay-secret"); res.Outcome != push.TokenDead {
		t.Fatalf("?answer=410 gave %+v", res)
	}
	if res := send(srv.URL+"?answer=400", "relay-secret"); res.Outcome != push.Rejected {
		t.Fatalf("?answer=400 gave %+v", res)
	}
}

// -answer with -times fails a fixed number of requests and then recovers,
// which is how the drill shows a retry that ends in a delivery.
func TestRelayStubAnswersOnDemand(t *testing.T) {
	s := &relayStub{answer: http.StatusServiceUnavailable, times: 2}
	got := []int{s.status(""), s.status(""), s.status(""), s.status("429")}
	want := []int{503, 503, 200, 429}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("answers %v, want %v", got, want)
		}
	}
	always := &relayStub{answer: http.StatusGone, times: -1}
	for range 3 {
		if code := always.status(""); code != http.StatusGone {
			t.Fatalf("an unlimited -answer stopped applying: %d", code)
		}
	}
}

func TestPushKeygenPrintsAPair(t *testing.T) {
	var out bytes.Buffer
	if err := pushKeygen(nil, &out); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "private_key: ") || !strings.Contains(s, "public_key:  ") {
		t.Fatalf("keygen printed %q", s)
	}
}
