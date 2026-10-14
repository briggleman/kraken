package unifi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const apiBase = "/proxy/network/api/s/default"

// The networks as the controller reported them on 2026-10-14, the VLAN id once
// as a number and once as a string (older controllers).
func networksHandler(t *testing.T, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == apiBase+"/rest/networkconf" {
			_, _ = io.WriteString(w, `{"data":[
				{"_id":"n1","name":"Trusted Network","ip_subnet":"192.168.0.100/24"},
				{"_id":"n2","name":"IoT Network","ip_subnet":"192.168.1.1/24","vlan":"2"},
				{"_id":"n20","name":"Kraken Games","ip_subnet":"192.168.20.1/24","vlan":20}]}`)
			return
		}
		if next == nil {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			return
		}
		next(w, r)
	}
}

func TestNetworkFor(t *testing.T) {
	c := newTestClient(t, networksHandler(t, nil))
	n, err := c.NetworkFor(context.Background(), "192.168.20.182")
	if err != nil {
		t.Fatalf("NetworkFor: %v", err)
	}
	if n.ID != "n20" || n.VLAN != 20 || n.Name != "Kraken Games" {
		t.Fatalf("got %+v, want Kraken Games n20 on VLAN 20", n)
	}
	if n, _ := c.NetworkFor(context.Background(), "192.168.1.40"); n.VLAN != 2 {
		t.Fatalf("string VLAN id not parsed: %+v", n)
	}
	if _, err := c.NetworkFor(context.Background(), "10.9.9.9"); err == nil {
		t.Fatal("an address in no network must be an error")
	}
}

func TestEnsureReservation(t *testing.T) {
	in := Reservation{MAC: "02:4B:AA:BB:CC:DD", Name: "kraken enshrouded-01", Note: "kraken", IP: "192.168.20.182", NetworkID: "n20"}
	cases := []struct {
		name       string
		users      string
		wantMethod string // "" = no write
		wantPath   string
	}{
		{name: "an unknown MAC is created", users: `[]`, wantMethod: http.MethodPost, wantPath: "/rest/user"},
		{
			name:       "a MAC the controller has seen is updated in place",
			users:      `[{"_id":"u7","mac":"02:4b:aa:bb:cc:dd","use_fixedip":false}]`,
			wantMethod: http.MethodPut, wantPath: "/rest/user/u7",
		},
		{
			name:  "an entry already pinned to the address is left alone",
			users: `[{"_id":"u7","mac":"02:4b:aa:bb:cc:dd","use_fixedip":true,"fixed_ip":"192.168.20.182","network_id":"n20"}]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var method, path string
			var body userEntry
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == apiBase+"/rest/user" {
					_, _ = io.WriteString(w, `{"data":`+tc.users+`}`)
					return
				}
				method, path = r.Method, strings.TrimPrefix(r.URL.Path, apiBase)
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &body)
				dataEnvelope(w, []any{})
			})
			if err := c.EnsureReservation(context.Background(), in); err != nil {
				t.Fatalf("EnsureReservation: %v", err)
			}
			if method != tc.wantMethod || path != tc.wantPath {
				t.Fatalf("wrote %s %s, want %s %s", method, path, tc.wantMethod, tc.wantPath)
			}
			if tc.wantMethod == "" {
				return
			}
			if body.MAC != "02:4b:aa:bb:cc:dd" || !body.UseFixedIP || body.FixedIP != in.IP || body.NetworkID != "n20" || body.Name != in.Name {
				t.Fatalf("unexpected body: %+v", body)
			}
		})
	}
}

func TestForgetClient(t *testing.T) {
	var got map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != apiBase+"/cmd/stamgr" {
			t.Errorf("unexpected req: %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		dataEnvelope(w, []any{})
	})
	if err := c.ForgetClient(context.Background(), "02:4B:AA:BB:CC:DD"); err != nil {
		t.Fatalf("ForgetClient: %v", err)
	}
	macs, _ := got["macs"].([]any)
	if got["cmd"] != "forget-sta" || len(macs) != 1 || macs[0] != "02:4b:aa:bb:cc:dd" {
		t.Fatalf("unexpected body: %v", got)
	}

	gone := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusNotFound) })
	if err := gone.ForgetClient(context.Background(), "02:4b:00:00:00:01"); err != nil {
		t.Fatalf("a MAC the controller doesn't know must not be an error: %v", err)
	}
}
