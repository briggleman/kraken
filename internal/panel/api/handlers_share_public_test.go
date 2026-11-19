package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// shareSeed adds a spec with a join block, an online node with a public WAN
// IP, and a running server on it, to the share fixture. The server carries an
// admin password and a LAN-only detail that no link may ever show.
func shareSeed(t *testing.T, e *accessEnv) {
	t.Helper()
	ctx := context.Background()
	sp := &spec.Spec{
		ID: "spec-pal", Name: "Palworld", Slug: "palworld", Version: 1,
		BannerURL: "https://cdn.example.net/hero.jpg",
		Platforms: []spec.Platform{{Kind: spec.LinuxNative, Image: "img"}},
		Install:   spec.Install{Script: "true"},
		Startup:   spec.Startup{Command: "run", Stop: spec.Stop{Type: spec.StopSignal, Value: "SIGINT"}},
		Variables: []spec.Variable{{Key: "SERVER_PASSWORD", Default: "fallback-pw", UserEditable: true}},
		Ports:     []spec.Port{{Name: "game", Protocol: spec.UDP, Default: 8211, Required: true}},
		Settings: spec.Settings{Groups: []spec.SettingGroup{{ID: "g", Fields: []spec.SettingField{
			{Key: "AdminPassword", Type: spec.FieldPassword},
		}}}},
		Join: &spec.Join{Method: spec.JoinAddress, Port: "game", Password: "SERVER_PASSWORD", Notes: "Join by address."},
	}
	if err := sp.Validate(); err != nil {
		t.Fatalf("seed spec invalid: %v", err)
	}
	if err := e.st.CreateSpec(ctx, sp); err != nil {
		t.Fatalf("seed spec: %v", err)
	}
	if err := e.st.CreateNode(ctx, &cluster.Node{
		ID: "node-1", Name: "abyss-secret-node", OS: cluster.OSLinux, Status: cluster.NodeOnline,
		Address: "192.168.1.88:9090", LANHost: "192.168.1.88", ExternalIP: "203.0.113.24",
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if err := e.st.CreateServer(ctx, &store.Server{
		ID: "srv-p", Name: "Saltmarrow <Crossing>", OwnerID: "olga", SpecID: "spec-pal", NodeID: "node-1",
		State: store.StateRunning, Ports: map[string]int{"game": 28211},
		Vars:     map[string]string{"SERVER_PASSWORD": "tidewater"},
		Settings: map[string]string{"AdminPassword": "admin-only-secret"},
		Players:  3, MaxPlayers: 10, PlayersKnown: true, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
}

// mintShare makes a link on srv-p and returns its path (/s/<token>).
func mintShare(t *testing.T, e *accessEnv, body map[string]any) string {
	t.Helper()
	rec := e.do(t, "olga", http.MethodPost, "/api/v1/servers/srv-p/share-links", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body.String())
	}
	var l struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	return strings.TrimPrefix(l.URL, invitePublic)
}

// getPublic fetches a public path with no session at all.
func getPublic(e *accessEnv, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "198.51.100.7:4242"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

var metaRe = regexp.MustCompile(`<meta (?:property|name)="([^"]+)" content="([^"]*)"`)

func metas(body string) map[string]string {
	out := map[string]string{}
	for _, m := range metaRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = m[2]
	}
	return out
}

func TestSharePage_StatusOnlyByDefault(t *testing.T) {
	e := newShareEnv(t)
	shareSeed(t, e)
	path := mintShare(t, e, map[string]any{})

	rec := getPublic(e, http.MethodGet, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Errorf("X-Robots-Tag = %q", got)
	}
	body := rec.Body.String()
	m := metas(body)
	if m["og:title"] != "Saltmarrow &lt;Crossing&gt;" {
		t.Errorf("og:title = %q (the name must be escaped)", m["og:title"])
	}
	if strings.Contains(body, "<Crossing>") {
		t.Error("server name reached the page unescaped")
	}
	if m["theme-color"] != "#ffc266" {
		t.Errorf("theme-color = %q, want the running gold", m["theme-color"])
	}
	if m["og:image"] != "https://cdn.example.net/hero.jpg" || m["twitter:card"] != "summary_large_image" {
		t.Errorf("image tags = %q / %q", m["og:image"], m["twitter:card"])
	}
	if m["og:url"] != invitePublic+path || !strings.Contains(m["og:site_name"], "kraken.example.com") {
		t.Errorf("og:url = %q, og:site_name = %q", m["og:url"], m["og:site_name"])
	}
	if want := "Palworld · running · 3 of 10 players"; m["og:description"] != want {
		t.Errorf("og:description = %q, want %q", m["og:description"], want)
	}
	// A status-only link shows no join values and leaks nothing else.
	for _, secret := range []string{"203.0.113.24", "28211", "tidewater", "admin-only-secret", "192.168.1.88", "abyss-secret-node", "srv-p", "node-1", "olga"} {
		if strings.Contains(body, secret) {
			t.Errorf("status-only page carries %q", secret)
		}
	}
}

func TestSharePage_JoinValuesFollowTheLinkFlags(t *testing.T) {
	e := newShareEnv(t)
	shareSeed(t, e)

	addr := getPublic(e, http.MethodGet, mintShare(t, e, map[string]any{"show_address": true})).Body.String()
	if !strings.Contains(addr, "203.0.113.24:28211") || strings.Contains(addr, "tidewater") {
		t.Errorf("address-only link: address shown=%v password shown=%v",
			strings.Contains(addr, "203.0.113.24:28211"), strings.Contains(addr, "tidewater"))
	}
	if d := metas(addr)["og:description"]; !strings.HasSuffix(d, "\naddress 203.0.113.24:28211") {
		t.Errorf("og:description = %q, want the address on its own line", d)
	}

	both := getPublic(e, http.MethodGet, mintShare(t, e, map[string]any{"show_address": true, "show_password": true})).Body.String()
	for _, want := range []string{"203.0.113.24:28211", "tidewater", "data-secret", "Copy all", "Join by address."} {
		if !strings.Contains(both, want) {
			t.Errorf("address+password link lacks %q", want)
		}
	}
	// The admin password is a setting of the same server; no flag reaches it.
	if strings.Contains(both, "admin-only-secret") || strings.Contains(both, "192.168.1.88") {
		t.Error("the page leaked the admin password or the node's LAN address")
	}
}

// The address prefers the server's DNS name, falls back to the WAN IP, and is
// left out entirely rather than show a LAN address.
func TestSharePage_AddressNeverLAN(t *testing.T) {
	e := newShareEnv(t)
	shareSeed(t, e)
	ctx := context.Background()
	path := mintShare(t, e, map[string]any{"show_address": true})

	sv, _ := e.st.GetServer(ctx, "srv-p")
	sv.DNS = &store.ServerDNS{Name: "pal.example.net"}
	_ = e.st.UpdateServer(ctx, sv)
	if body := getPublic(e, http.MethodGet, path).Body.String(); !strings.Contains(body, "pal.example.net:28211") {
		t.Error("DNS name not preferred")
	}

	sv.DNS = nil
	_ = e.st.UpdateServer(ctx, sv)
	n, _ := e.st.GetNode(ctx, "node-1")
	n.ExternalIP = ""
	n.PublicHost = "10.0.0.5"
	_ = e.st.UpdateNode(ctx, n)
	body := getPublic(e, http.MethodGet, path).Body.String()
	if strings.Contains(body, "10.0.0.5") || strings.Contains(body, "192.168.1.88") || strings.Contains(body, "28211") {
		t.Error("a LAN host reached the public page")
	}
}

func TestSharePage_UnreachableAndStopped(t *testing.T) {
	e := newShareEnv(t)
	shareSeed(t, e)
	ctx := context.Background()
	path := mintShare(t, e, map[string]any{})

	n, _ := e.st.GetNode(ctx, "node-1")
	n.Status = cluster.NodeOffline
	_ = e.st.UpdateNode(ctx, n)
	m := metas(getPublic(e, http.MethodGet, path).Body.String())
	if m["theme-color"] != "#9d8cff" || !strings.Contains(m["og:description"], "can&#39;t reach") {
		t.Errorf("unreachable: theme %q, description %q", m["theme-color"], m["og:description"])
	}
	if strings.Contains(m["og:description"], "players") {
		t.Error("an unreachable server must not claim a player count")
	}

	n.Status = cluster.NodeOnline
	_ = e.st.UpdateNode(ctx, n)
	sv, _ := e.st.GetServer(ctx, "srv-p")
	sv.State = store.StateCrashed
	_ = e.st.UpdateServer(ctx, sv)
	m = metas(getPublic(e, http.MethodGet, path).Body.String())
	if m["theme-color"] != "#7d7160" || m["og:description"] != "Palworld · stopped" {
		t.Errorf("stopped: theme %q, description %q", m["theme-color"], m["og:description"])
	}
}

// Unknown, revoked and expired links answer the same plain 404, with no card
// tags for a chat service to draw. Opening a live link never consumes it.
func TestSharePage_NotFoundAndNeverConsumed(t *testing.T) {
	e := newShareEnv(t)
	shareSeed(t, e)
	path := mintShare(t, e, map[string]any{})

	for i := 0; i < 3; i++ {
		if rec := getPublic(e, http.MethodGet, path); rec.Code != http.StatusOK {
			t.Fatalf("fetch %d: %d — a share link must survive being opened", i, rec.Code)
		}
	}
	if rec := getPublic(e, http.MethodHead, path); rec.Code != http.StatusOK {
		t.Fatalf("HEAD: %d", rec.Code)
	}
	hash := store.HashToken(strings.TrimPrefix(path, "/s/"))
	l, _ := e.st.GetShareLinkByHash(context.Background(), hash)
	if l.LastSeenAt == nil {
		t.Error("opening the link did not record last_seen_at")
	}

	check := func(name, p string) {
		t.Helper()
		rec := getPublic(e, http.MethodGet, p)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d, want 404", name, rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "og:") || !strings.Contains(body, "Link not available") {
			t.Fatalf("%s: 404 page carries card tags or the wrong copy", name)
		}
	}
	check("unknown", "/s/not-a-real-token")

	if rec := e.do(t, "olga", http.MethodDelete, "/api/v1/servers/srv-p/share-links/"+l.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", rec.Code)
	}
	check("revoked", path)

	past := time.Now().Add(-time.Minute)
	const expiredToken = "an-expired-share-token"
	if err := e.st.CreateShareLink(context.Background(), &store.ShareLink{
		ID: "expired-link", ServerID: "srv-p", TokenHash: store.HashToken(expiredToken),
		CreatedAt: time.Now().Add(-48 * time.Hour), ExpiresAt: &past,
	}); err != nil {
		t.Fatalf("seed expired link: %v", err)
	}
	check("expired", "/s/"+expiredToken)
}

func TestSharePage_StaticAssets(t *testing.T) {
	e := newShareEnv(t)
	for _, p := range []string{"/s/static/share.css", "/s/static/share.js"} {
		rec := getPublic(e, http.MethodGet, p)
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}
