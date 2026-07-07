package unifi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "key-abc", "", false)
	c.http = srv.Client() // use the test server's client (plain http)
	return c
}

func dataEnvelope(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]string{"rc": "ok"}, "data": result})
}

func TestNew_BaseAndSite(t *testing.T) {
	c := New("https://192.168.1.1/", "k", "", false)
	if c.base != "https://192.168.1.1/proxy/network/api/s/default" {
		t.Fatalf("unexpected base: %s", c.base)
	}
	if c2 := New("https://gw", "k", "lab", false); c2.base != "https://gw/proxy/network/api/s/lab" {
		t.Fatalf("unexpected site base: %s", c2.base)
	}
}

func TestEnsureForward_CreateSendsRule(t *testing.T) {
	var got PortForward
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "key-abc" {
			t.Errorf("missing api key header, got %q", r.Header.Get("X-API-KEY"))
		}
		if r.Method != http.MethodPost || r.URL.Path != "/proxy/network/api/s/default/rest/portforward" {
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		dataEnvelope(w, []PortForward{{ID: "rule1"}})
	})

	id, err := c.EnsureForward(context.Background(), Forward{Name: "leviathan game", Port: 28000, LANIP: "192.168.0.75", Proto: "udp", Enabled: true})
	if err != nil {
		t.Fatalf("EnsureForward: %v", err)
	}
	if id != "rule1" {
		t.Fatalf("expected rule1, got %q", id)
	}
	if got.DstPort != "28000" || got.FwdPort != "28000" || got.Fwd != "192.168.0.75" || got.Proto != "udp" {
		t.Fatalf("unexpected rule body: %+v", got)
	}
	if got.PfwdInterface != "wan" || got.Src != "any" || !got.Enabled {
		t.Fatalf("unexpected rule defaults: %+v", got)
	}
}

func TestEnsureForward_UpdateUsesPut(t *testing.T) {
	var method, path string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		dataEnvelope(w, []PortForward{{ID: "rule1"}})
	})
	if _, err := c.EnsureForward(context.Background(), Forward{ID: "rule1", Port: 1, LANIP: "10.0.0.2", Proto: "tcp"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if method != http.MethodPut || path != "/proxy/network/api/s/default/rest/portforward/rule1" {
		t.Fatalf("expected PUT to rule path, got %s %s", method, path)
	}
}

func TestDeleteForward_IgnoresMissing(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"meta":{"rc":"error"}}`))
	})
	if err := c.DeleteForward(context.Background(), "gone"); err != nil {
		t.Fatalf("DeleteForward should ignore 404, got %v", err)
	}
}

func TestGatewayWANIP(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		dataEnvelope(w, []map[string]any{
			{"subsystem": "wlan", "status": "ok"},
			{"subsystem": "wan", "status": "ok", "wan_ip": "203.0.113.5"},
		})
	})
	ip, err := c.GatewayWANIP(context.Background())
	if err != nil {
		t.Fatalf("GatewayWANIP: %v", err)
	}
	if ip != "203.0.113.5" {
		t.Fatalf("expected 203.0.113.5, got %q", ip)
	}
}
