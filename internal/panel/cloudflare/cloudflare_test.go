package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient returns a Client pointed at a fake Cloudflare API and the handler's
// recorded last request body.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{token: "test-token", base: srv.URL, http: srv.Client()}
}

func writeEnvelope(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

func TestListZonesAndZoneFor(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing bearer auth, got %q", r.Header.Get("Authorization"))
		}
		writeEnvelope(w, []Zone{
			{ID: "z1", Name: "example.com"},
			{ID: "z2", Name: "sub.example.com"},
			{ID: "z3", Name: "other.net"},
		})
	})

	// Longest-suffix match: play.sub.example.com → sub.example.com (z2), not example.com.
	z, err := c.ZoneFor(context.Background(), "play.sub.example.com")
	if err != nil {
		t.Fatalf("ZoneFor: %v", err)
	}
	if z.ID != "z2" {
		t.Fatalf("expected zone z2 (sub.example.com), got %+v", z)
	}
	// Apex of a zone matches that zone.
	if z, _ := c.ZoneFor(context.Background(), "example.com"); z.ID != "z1" {
		t.Fatalf("expected z1 for apex, got %+v", z)
	}
	// No match errors.
	if _, err := c.ZoneFor(context.Background(), "game.nope.org"); err == nil {
		t.Fatal("expected error for unmatched zone")
	}
}

func TestCreateHostRecord_AvsCNAME(t *testing.T) {
	var got dnsRecord
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		writeEnvelope(w, map[string]string{"id": "rec1"})
	})

	// IPv4 → A record, DNS-only.
	id, err := c.CreateHostRecord(context.Background(), "z1", "play.example.com", "203.0.113.7")
	if err != nil {
		t.Fatalf("CreateHostRecord: %v", err)
	}
	if id != "rec1" {
		t.Fatalf("expected record id rec1, got %q", id)
	}
	if got.Type != "A" || got.Content != "203.0.113.7" || got.Name != "play.example.com" {
		t.Fatalf("unexpected A record: %+v", got)
	}
	if got.Proxied == nil || *got.Proxied {
		t.Fatalf("host record must be DNS-only (proxied=false), got %+v", got.Proxied)
	}

	// Hostname → CNAME.
	if _, err := c.CreateHostRecord(context.Background(), "z1", "play.example.com", "node1.host.net"); err != nil {
		t.Fatalf("CreateHostRecord (cname): %v", err)
	}
	if got.Type != "CNAME" || got.Content != "node1.host.net" {
		t.Fatalf("expected CNAME, got %+v", got)
	}
}

func TestUpdateHostRecord(t *testing.T) {
	var got dnsRecord
	var method, path string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		writeEnvelope(w, map[string]string{"id": "rec1"})
	})

	// Re-point to a new IPv4 → PUT in place, A record, DNS-only, same record id.
	if err := c.UpdateHostRecord(context.Background(), "z1", "rec1", "play.example.com", "198.51.100.9"); err != nil {
		t.Fatalf("UpdateHostRecord: %v", err)
	}
	if method != http.MethodPut || path != "/zones/z1/dns_records/rec1" {
		t.Fatalf("expected PUT /zones/z1/dns_records/rec1, got %s %s", method, path)
	}
	if got.Type != "A" || got.Content != "198.51.100.9" || got.Name != "play.example.com" {
		t.Fatalf("unexpected updated record: %+v", got)
	}
	if got.Proxied == nil || *got.Proxied {
		t.Fatalf("updated host record must stay DNS-only, got %+v", got.Proxied)
	}

	// Switching to a hostname re-derives the type to CNAME.
	if err := c.UpdateHostRecord(context.Background(), "z1", "rec1", "play.example.com", "node2.host.net"); err != nil {
		t.Fatalf("UpdateHostRecord (cname): %v", err)
	}
	if got.Type != "CNAME" || got.Content != "node2.host.net" {
		t.Fatalf("expected CNAME after switch, got %+v", got)
	}
}

func TestCreateSRVRecord(t *testing.T) {
	var got dnsRecord
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		writeEnvelope(w, map[string]string{"id": "srv1"})
	})

	id, err := c.CreateSRVRecord(context.Background(), "z1", "minecraft", "tcp", "play.example.com", 25565)
	if err != nil {
		t.Fatalf("CreateSRVRecord: %v", err)
	}
	if id != "srv1" {
		t.Fatalf("expected srv1, got %q", id)
	}
	if got.Type != "SRV" || got.Name != "_minecraft._tcp.play.example.com" {
		t.Fatalf("unexpected SRV name/type: %+v", got)
	}
	if got.Data == nil || got.Data.Port != 25565 || got.Data.Target != "play.example.com" {
		t.Fatalf("unexpected SRV data: %+v", got.Data)
	}
}

func TestNormalizeService(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"enshrouded":        "enshrouded",
		"_enshrouded":       "enshrouded",
		"_enshrouded._udp":  "enshrouded", // what the old placeholder suggested
		" Enshrouded._TCP ": "enshrouded",
		"my-game":           "my-game",
	} {
		got, err := NormalizeService(in)
		if err != nil || got != want {
			t.Errorf("NormalizeService(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"two words", "a.b", "-lead", "trail-", "x_y"} {
		if _, err := NormalizeService(bad); err == nil {
			t.Errorf("NormalizeService(%q) should fail", bad)
		}
	}
}

// A record already at the name — left by a publish that failed half-way, or
// made by hand — is updated in place. Creating a second one is what Cloudflare
// refused, which used to fail every publish after the first failure.
func TestEnsureHostRecord(t *testing.T) {
	cases := []struct {
		name        string
		existing    string
		wantMethod  string
		wantCreated bool
		wantID      string
	}{
		{name: "adopts an existing A record", existing: `[{"id":"old","type":"A","name":"play.example.com"}]`, wantMethod: http.MethodPut, wantID: "old"},
		{name: "adopts a CNAME, ignoring a TXT", existing: `[{"id":"t","type":"TXT","name":"play.example.com"},{"id":"c","type":"CNAME","name":"play.example.com"}]`, wantMethod: http.MethodPut, wantID: "c"},
		{name: "creates when none", existing: `[]`, wantMethod: http.MethodPost, wantCreated: true, wantID: "new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var method, query string
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					query = r.URL.Query().Get("name")
					_, _ = io.WriteString(w, `{"success":true,"errors":[],"result":`+tc.existing+`}`)
					return
				}
				method = r.Method
				writeEnvelope(w, map[string]string{"id": "new"})
			})
			id, created, err := c.EnsureHostRecord(context.Background(), "z1", "play.example.com", "107.221.197.247")
			if err != nil {
				t.Fatalf("EnsureHostRecord: %v", err)
			}
			if query != "play.example.com" || method != tc.wantMethod || created != tc.wantCreated || id != tc.wantID {
				t.Fatalf("query %q, %s, created=%v, id %q; want %s created=%v id %q", query, method, created, id, tc.wantMethod, tc.wantCreated, tc.wantID)
			}
		})
	}
}

func TestEnsureSRVRecordUpdatesInPlace(t *testing.T) {
	var method, path, query string
	var got dnsRecord
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			query = r.URL.Query().Get("name")
			_, _ = io.WriteString(w, `{"success":true,"errors":[],"result":[{"id":"s1","type":"SRV","name":"_enshrouded._udp.play.example.com"}]}`)
			return
		}
		method, path = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		writeEnvelope(w, map[string]string{"id": "s1"})
	})
	id, created, err := c.EnsureSRVRecord(context.Background(), "z1", "enshrouded", "udp", "play.example.com", 28000)
	if err != nil || id != "s1" || created {
		t.Fatalf("got %q created=%v err=%v", id, created, err)
	}
	if query != "_enshrouded._udp.play.example.com" || method != http.MethodPut || path != "/zones/z1/dns_records/s1" {
		t.Fatalf("looked up %q, wrote %s %s", query, method, path)
	}
	if got.Name != "_enshrouded._udp.play.example.com" || got.Data == nil || got.Data.Port != 28000 {
		t.Fatalf("record %+v", got)
	}
}

// Zones past the first page are found: a token with more than 50 zones used
// to miss every one after the 50th.
func TestListZonesPages(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		zone := `{"id":"z-` + page + `","name":"zone` + page + `.com"}`
		_, _ = io.WriteString(w, `{"success":true,"errors":[],"result":[`+zone+`],"result_info":{"page":`+page+`,"total_pages":3}}`)
	})
	zones, err := c.ListZones(context.Background())
	if err != nil || len(zones) != 3 || zones[2].ID != "z-3" {
		t.Fatalf("got %+v, %v; want three pages of zones", zones, err)
	}
	if z, err := c.ZoneFor(context.Background(), "play.zone3.com"); err != nil || z.ID != "z-3" {
		t.Fatalf("ZoneFor on page 3: %+v %v", z, err)
	}
}

func TestDeleteRecord_IgnoresMissing(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []map[string]any{{"code": 81044, "message": "Record does not exist."}},
		})
	})
	if err := c.DeleteRecord(context.Background(), "z1", "gone"); err != nil {
		t.Fatalf("DeleteRecord should ignore 81044 (missing), got %v", err)
	}
}

func TestAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []map[string]any{{"code": 9109, "message": "Invalid access token"}},
		})
	})
	if _, err := c.ListZones(context.Background()); err == nil {
		t.Fatal("expected error on unsuccessful response")
	}
}
