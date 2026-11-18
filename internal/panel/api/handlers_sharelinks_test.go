package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

type shareLinkResp struct {
	ID           string     `json:"id"`
	Label        string     `json:"label"`
	ShowAddress  bool       `json:"show_address"`
	ShowPassword bool       `json:"show_password"`
	CreatedBy    string     `json:"created_by"`
	ExpiresAt    *time.Time `json:"expires_at"`
	State        string     `json:"state"`
	URL          string     `json:"url"`
}

type shareListResp struct {
	Links        []shareLinkResp `json:"links"`
	PublicURLSet bool            `json:"public_url_set"`
}

// newShareEnv is the access fixture with a public address set, so links can
// be minted.
func newShareEnv(t *testing.T) *accessEnv {
	t.Helper()
	e := newAccessEnv(t)
	if rec := e.do(t, "admin", http.MethodPut, "/api/v1/settings", map[string]any{"public_url": invitePublic}); rec.Code != http.StatusOK {
		t.Fatalf("public url: %d %s", rec.Code, rec.Body.String())
	}
	return e
}

func TestShareLinks_MintListRevoke(t *testing.T) {
	e := newShareEnv(t)
	rec := e.do(t, "olga", http.MethodPost, "/api/v1/servers/srv-a/share-links",
		map[string]any{"label": "  pinned in #general ", "show_address": true, "expires_in_days": 7})
	if rec.Code != http.StatusCreated {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body.String())
	}
	var minted shareLinkResp
	_ = json.Unmarshal(rec.Body.Bytes(), &minted)
	if !strings.HasPrefix(minted.URL, invitePublic+"/s/") || len(minted.URL) < len(invitePublic)+3+40 {
		t.Fatalf("url = %q, want %s/s/<token>", minted.URL, invitePublic)
	}
	if minted.Label != "pinned in #general" || !minted.ShowAddress || minted.ShowPassword || minted.State != "live" || minted.CreatedBy != "olga" {
		t.Fatalf("minted = %+v", minted)
	}
	if minted.ExpiresAt == nil || time.Until(*minted.ExpiresAt) < 6*24*time.Hour {
		t.Fatalf("expires_at = %v, want about 7 days out", minted.ExpiresAt)
	}

	// The stored row holds the digest, never the token.
	token := strings.TrimPrefix(minted.URL, invitePublic+"/s/")
	got, err := e.st.GetShareLinkByHash(context.Background(), store.HashToken(token))
	if err != nil || got.ID != minted.ID || got.TokenHash == token {
		t.Fatalf("stored link = %+v, %v", got, err)
	}

	// The list never shows a URL again.
	rec = e.do(t, "olga", http.MethodGet, "/api/v1/servers/srv-a/share-links", nil)
	var list shareListResp
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list.Links) != 1 || list.Links[0].ID != minted.ID || list.Links[0].URL != "" || !list.PublicURLSet {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}

	// Nothing in the audit log carries the token.
	entries, _ := e.st.ListAudit(context.Background(), 0)
	var sawMint bool
	for _, a := range entries {
		if strings.Contains(a.Action+a.Path, token) {
			t.Fatalf("audit entry carries the token: %+v", a)
		}
		if a.Action == "server.share:"+minted.ID+"+address" {
			sawMint = true
		}
	}
	if !sawMint {
		t.Fatal("no server.share audit entry naming what the link reveals")
	}

	if rec := e.do(t, "olga", http.MethodDelete, "/api/v1/servers/srv-a/share-links/"+minted.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, "olga", http.MethodDelete, "/api/v1/servers/srv-a/share-links/"+minted.ID, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second revoke: %d, want 404", rec.Code)
	}
	rec = e.do(t, "olga", http.MethodGet, "/api/v1/servers/srv-a/share-links", nil)
	list = shareListResp{}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Links) != 0 {
		t.Fatalf("revoked link still listed: %s", rec.Body.String())
	}
	if l, _ := e.st.GetShareLinkByHash(context.Background(), store.HashToken(token)); l.Live(time.Now()) {
		t.Fatal("revoked link is still live")
	}
}

// Sharing needs server.config on a server the caller can reach. A Read-only
// user is refused outright; an Operator who cannot see the server gets the
// same 404 as for a server that does not exist.
func TestShareLinks_Permissions(t *testing.T) {
	e := newShareEnv(t)
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		if rec := e.do(t, "ron", m, "/api/v1/servers/srv-a/share-links", map[string]any{}); rec.Code != http.StatusForbidden {
			t.Errorf("read-only %s: %d, want 403", m, rec.Code)
		}
		if rec := e.do(t, "otto", m, "/api/v1/servers/srv-a/share-links", map[string]any{}); rec.Code != http.StatusNotFound {
			t.Errorf("operator without access %s: %d, want 404", m, rec.Code)
		}
	}
	if rec := e.do(t, "admin", http.MethodPost, "/api/v1/servers/srv-a/share-links", map[string]any{}); rec.Code != http.StatusCreated {
		t.Fatalf("admin mint: %d %s", rec.Code, rec.Body.String())
	}
	var list shareListResp
	_ = json.Unmarshal(e.do(t, "admin", http.MethodGet, "/api/v1/servers/srv-a/share-links", nil).Body.Bytes(), &list)
	if len(list.Links) != 1 {
		t.Fatalf("admin list: %+v", list)
	}
	id := list.Links[0].ID
	if rec := e.do(t, "otto", http.MethodDelete, "/api/v1/servers/srv-a/share-links/"+id, nil); rec.Code != http.StatusNotFound {
		t.Errorf("operator without access revoke: %d, want 404", rec.Code)
	}
	if rec := e.do(t, "olga", http.MethodDelete, "/api/v1/servers/srv-a/share-links/not-a-link", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown link: %d, want 404", rec.Code)
	}
}

func TestShareLinks_Validation(t *testing.T) {
	e := newShareEnv(t)
	for name, body := range map[string]any{
		"label too long":   map[string]any{"label": strings.Repeat("x", 61)},
		"label two lines":  map[string]any{"label": "a\nb"},
		"negative expiry":  map[string]any{"expires_in_days": -1},
		"expiry too long":  map[string]any{"expires_in_days": 366},
		"malformed body":   "not json",
		"wrong field type": map[string]any{"show_address": "yes"},
	} {
		if rec := e.do(t, "olga", http.MethodPost, "/api/v1/servers/srv-a/share-links", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	rec := e.do(t, "olga", http.MethodPost, "/api/v1/servers/srv-a/share-links", map[string]any{"label": strings.Repeat("é", 60)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("60 runes is allowed: %d %s", rec.Code, rec.Body.String())
	}
	var never shareLinkResp
	_ = json.Unmarshal(rec.Body.Bytes(), &never)
	if never.ExpiresAt != nil {
		t.Fatalf("expires_in_days 0 must not expire, got %v", never.ExpiresAt)
	}
}

// A link is an absolute URL pasted elsewhere, so it is never built from the
// request's Host: without a configured public address there is no link.
func TestShareLinks_NeedAPublicAddress(t *testing.T) {
	e := newAccessEnv(t)
	rec := e.do(t, "olga", http.MethodPost, "/api/v1/servers/srv-a/share-links", map[string]any{})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "public address") {
		t.Fatalf("mint without public url: %d %s", rec.Code, rec.Body.String())
	}
	var list shareListResp
	_ = json.Unmarshal(e.do(t, "olga", http.MethodGet, "/api/v1/servers/srv-a/share-links", nil).Body.Bytes(), &list)
	if list.PublicURLSet {
		t.Fatal("public_url_set must be false")
	}
}

func TestShareLinks_CapPerServer(t *testing.T) {
	e := newShareEnv(t)
	var last string
	for i := 0; i < 25; i++ {
		rec := e.do(t, "olga", http.MethodPost, "/api/v1/servers/srv-a/share-links", map[string]any{})
		if rec.Code != http.StatusCreated {
			t.Fatalf("mint %d: %d %s", i, rec.Code, rec.Body.String())
		}
		var l shareLinkResp
		_ = json.Unmarshal(rec.Body.Bytes(), &l)
		last = l.ID
	}
	if rec := e.do(t, "olga", http.MethodPost, "/api/v1/servers/srv-a/share-links", map[string]any{}); rec.Code != http.StatusConflict {
		t.Fatalf("26th link: %d, want 409", rec.Code)
	}
	if rec := e.do(t, "olga", http.MethodDelete, "/api/v1/servers/srv-a/share-links/"+last, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec := e.do(t, "olga", http.MethodPost, "/api/v1/servers/srv-a/share-links", map[string]any{}); rec.Code != http.StatusCreated {
		t.Fatalf("after revoking one: %d, want 201", rec.Code)
	}
}
