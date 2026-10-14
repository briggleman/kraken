package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/briggleman/kraken/internal/panel/api"
)

// fakeCloudflare is one zone (example.com) holding DNS records in memory, the
// subset of the v4 API the Panel's DNS paths use. failSRV makes SRV creates
// fail the way a rejected record does.
type fakeCloudflare struct {
	mu      sync.Mutex
	next    int
	records map[string]map[string]any // id → record
	failSRV bool
}

func newFakeCloudflare(t *testing.T) *fakeCloudflare {
	f := &fakeCloudflare{records: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Cleanup(api.SetCloudflareBaseForTest(srv.URL))
	return f
}

func (f *fakeCloudflare) seed(typ, name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("seed-%d", f.next)
	f.records[id] = map[string]any{"id": id, "type": typ, "name": name}
	return id
}

func (f *fakeCloudflare) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

func (f *fakeCloudflare) setFailSRV() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failSRV = true
}

func (f *fakeCloudflare) named(name string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, r := range f.records {
		if r["name"] == name {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(result any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
	}
	fail := func(msg string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 9000, "message": msg}}})
	}
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	path := r.URL.Path
	switch {
	case path == "/zones":
		ok([]map[string]string{{"id": "z1", "name": "example.com"}})
	case path == "/zones/z1/dns_records" && r.Method == http.MethodGet:
		var out []map[string]any
		for _, rec := range f.records {
			if rec["name"] == r.URL.Query().Get("name") {
				out = append(out, rec)
			}
		}
		ok(out)
	case path == "/zones/z1/dns_records" && r.Method == http.MethodPost:
		if f.failSRV && body["type"] == "SRV" {
			fail("DNS Validation Error")
			return
		}
		for _, rec := range f.records {
			if rec["name"] == body["name"] && rec["type"] == body["type"] {
				fail("An identical record already exists.")
				return
			}
		}
		f.next++
		id := fmt.Sprintf("rec-%d", f.next)
		body["id"] = id
		f.records[id] = body
		ok(map[string]string{"id": id})
	case strings.HasPrefix(path, "/zones/z1/dns_records/"):
		id := strings.TrimPrefix(path, "/zones/z1/dns_records/")
		if _, found := f.records[id]; !found {
			fail("Record does not exist. (81044)")
			return
		}
		if r.Method == http.MethodDelete {
			delete(f.records, id)
		} else {
			body["id"] = id
			f.records[id] = body
		}
		ok(map[string]string{"id": id})
	default:
		http.Error(w, "unexpected "+r.Method+" "+path, http.StatusTeapot)
	}
}

func dnsSetup(t *testing.T) (http.Handler, string, string, *fakeCloudflare) {
	t.Helper()
	f := newFakeCloudflare(t)
	h := newTestServer(t)
	token := login(t, h)
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"cloudflare_api_token": "tok"}); rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}
	return h, token, setupServer(t, h, token), f
}

func savedDNS(t *testing.T, h http.Handler, token, id string) (name, service string) {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+id+"/dns", token, nil)
	var v struct {
		DNS *struct {
			Name    string `json:"name"`
			Service string `json:"service"`
		} `json:"dns"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.DNS == nil {
		return "", ""
	}
	return v.DNS.Name, v.DNS.Service
}

// The reported bug (#431): publish, refresh, and the name was gone. A publish
// now survives the reload, and the service the old placeholder suggested
// ("_enshrouded._udp") makes one well-formed SRV record.
func TestPublishDNSPersists(t *testing.T) {
	h, token, id, f := dnsSetup(t)
	rec := do(t, h, http.MethodPut, "/api/v1/servers/"+id+"/dns", token, map[string]any{"name": "play.example.com", "service": "_enshrouded._udp"})
	if rec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	if name, svc := savedDNS(t, h, token, id); name != "play.example.com" || svc != "enshrouded" {
		t.Fatalf("after reload: name %q service %q", name, svc)
	}
	if len(f.named("play.example.com")) != 1 || len(f.named("_enshrouded._udp.play.example.com")) != 1 {
		t.Fatalf("records for the name: %v / %v", f.named("play.example.com"), f.named("_enshrouded._udp.play.example.com"))
	}

	// Publishing again changes nothing: the records are updated, not duplicated.
	if rec := do(t, h, http.MethodPut, "/api/v1/servers/"+id+"/dns", token, map[string]any{"name": "play.example.com", "service": "enshrouded"}); rec.Code != http.StatusOK {
		t.Fatalf("re-publish: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(); n != 2 {
		t.Fatalf("re-publish left %d records, want 2", n)
	}
}

// A record already at the name — what a half-failed publish used to leave —
// is adopted. It used to make every later publish fail, forever.
func TestPublishDNSAdoptsAnOrphan(t *testing.T) {
	h, token, id, f := dnsSetup(t)
	f.seed("A", "play.example.com")
	if rec := do(t, h, http.MethodPut, "/api/v1/servers/"+id+"/dns", token, map[string]any{"name": "play.example.com"}); rec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.named("play.example.com"); len(got) != 1 {
		t.Fatalf("records for the name: %v; want the orphan adopted, not a second one", got)
	}
}

// A failed SRV record leaves nothing behind: the host record this publish
// created is removed again, nothing is saved, and the reason reaches the UI
// (424, which Cloudflare in front of the Panel passes through).
func TestPublishDNSIsAllOrNothing(t *testing.T) {
	h, token, id, f := dnsSetup(t)
	f.setFailSRV()
	rec := do(t, h, http.MethodPut, "/api/v1/servers/"+id+"/dns", token, map[string]any{"name": "play.example.com", "service": "enshrouded"})
	if rec.Code != http.StatusFailedDependency || !strings.Contains(rec.Body.String(), "_enshrouded._udp.play.example.com") {
		t.Fatalf("publish: %d %s; want 424 naming the SRV record", rec.Code, rec.Body.String())
	}
	if n := f.count(); n != 0 {
		t.Fatalf("%d records left behind", n)
	}
	if name, _ := savedDNS(t, h, token, id); name != "" {
		t.Fatalf("a failed publish saved %q", name)
	}
}

func TestPublishDNSRejectsABadService(t *testing.T) {
	h, token, id, f := dnsSetup(t)
	rec := do(t, h, http.MethodPut, "/api/v1/servers/"+id+"/dns", token, map[string]any{"name": "play.example.com", "service": "two words"})
	if rec.Code != http.StatusBadRequest || f.count() != 0 {
		t.Fatalf("got %d, %d records; want 400 and nothing written", rec.Code, f.count())
	}
}
