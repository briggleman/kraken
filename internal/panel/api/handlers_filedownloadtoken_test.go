package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// The Agent's fake runtime seeds every server with this file; see
// internal/agent/fake.go.
const (
	fakeCfgPath = "/data/server.cfg"
	fakeCfgBody = "# fake content for /data/server.cfg\nkey=value\n"
	fakeSavePth = "/data/saves"
)

type downloadEnv struct {
	srv    *api.Server
	h      http.Handler
	st     *memory.Store
	token  string // the bootstrap admin's session
	server string // a deployed server on the fake node
	nodeID string
}

// newDownloadEnv brings up the Panel with a fake Agent behind it and one
// installed server, which is what a download token has to be minted against.
func newDownloadEnv(t *testing.T) *downloadEnv {
	t.Helper()
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr := startFakeAgent(t, "node-download-token")
	nodeID := registerNode(t, h, token, addr)
	pollNode(t, h, token, nodeID)
	specID := createSpecWithBackup(t, h, token, "download-token-spec", nil)
	serverID := createServerFromSpec(t, h, token, specID, "abyssal-01")
	waitInstalled(t, st, serverID)
	return &downloadEnv{srv: srv, h: h, st: st, token: token, server: serverID, nodeID: nodeID}
}

type mintedToken struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	Kind      string `json:"kind"`
	ExpiresIn int    `json:"expires_in_seconds"`
}

func (e *downloadEnv) mint(t *testing.T, sessionToken, serverID string, body any) (mintedToken, int) {
	t.Helper()
	rec := do(t, e.h, http.MethodPost, "/api/v1/servers/"+serverID+"/files/download-token", sessionToken, body)
	var out mintedToken
	if rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode minted token: %v (body %s)", err, rec.Body.String())
		}
	}
	return out, rec.Code
}

// A token is worth nothing without the permission the raw route itself carries.
func TestDownloadTokenMintRequiresFilesReadPermission(t *testing.T) {
	e := newDownloadEnv(t)
	ctx := context.Background()
	// Read-only holds server.view but NOT server.files.read.
	if err := e.st.CreateUser(ctx, &store.User{ID: "viewer", Username: "viewer", RoleID: rbac.RoleReadOnly, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := e.st.CreateSession(ctx, &store.Session{Token: "viewer-token", UserID: "viewer", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, code := e.mint(t, "viewer-token", e.server, map[string]any{"path": fakeCfgPath}); code != http.StatusForbidden {
		t.Fatalf("read-only mint: got %d, want 403", code)
	}
	if _, code := e.mint(t, "", e.server, map[string]any{"path": fakeCfgPath}); code != http.StatusUnauthorized {
		t.Fatalf("anonymous mint: got %d, want 401", code)
	}
	if _, code := e.mint(t, e.token, e.server, map[string]any{"path": fakeCfgPath}); code != http.StatusCreated {
		t.Fatalf("admin mint: got %d, want 201", code)
	}
}

// The mint refuses a body that names no path, both shapes at once, or one that
// climbs out of the tree.
func TestDownloadTokenMintRejectsBadPaths(t *testing.T) {
	e := newDownloadEnv(t)
	bad := []any{
		map[string]any{},
		map[string]any{"path": fakeCfgPath, "paths": []string{fakeCfgPath}},
		map[string]any{"path": "../../etc/passwd"},
		map[string]any{"path": ""},
		map[string]any{"paths": []string{fakeCfgPath, ".."}},
	}
	for _, b := range bad {
		if _, code := e.mint(t, e.token, e.server, b); code != http.StatusBadRequest {
			t.Errorf("mint %v: got %d, want 400", b, code)
		}
	}
}

// The happy path end to end: the minted URL streams the file's real bytes with
// the Content-Disposition that names the save, and it works exactly once.
func TestDownloadTokenStreamsOnceThenIsGone(t *testing.T) {
	e := newDownloadEnv(t)
	tok, code := e.mint(t, e.token, e.server, map[string]any{"path": fakeCfgPath})
	if code != http.StatusCreated {
		t.Fatalf("mint: got %d, want 201", code)
	}
	if tok.Kind != "raw" || tok.ExpiresIn != 60 {
		t.Fatalf("minted %+v, want kind raw and a 60s life", tok)
	}
	if !strings.Contains(tok.URL, "path=") {
		t.Fatalf("minted url %q does not name the path", tok.URL)
	}

	// No session header at all — the token is the whole authority.
	rec := do(t, e.h, http.MethodGet, tok.URL, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem: got %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != fakeCfgBody {
		t.Fatalf("streamed %q, want the file's bytes %q", got, fakeCfgBody)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="server.cfg"` {
		t.Fatalf("Content-Disposition = %q, want the file's name", cd)
	}

	// Single use: the same URL is dead the moment it has been redeemed.
	if again := do(t, e.h, http.MethodGet, tok.URL, "", nil); again.Code != http.StatusUnauthorized {
		t.Fatalf("second redeem: got %d, want 401", again.Code)
	}
}

// A folder download is the zip route, reachable by GET only with a token.
func TestDownloadTokenStreamsZip(t *testing.T) {
	e := newDownloadEnv(t)
	tok, code := e.mint(t, e.token, e.server, map[string]any{"paths": []string{fakeSavePth}})
	if code != http.StatusCreated {
		t.Fatalf("mint: got %d, want 201", code)
	}
	if tok.Kind != "zip" {
		t.Fatalf("kind = %q, want zip", tok.Kind)
	}
	rec := do(t, e.h, http.MethodGet, tok.URL, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem zip: got %d, body %s", rec.Code, rec.Body.String())
	}
	// A one-path zip is named after that path: on a real navigation
	// Content-Disposition is the only thing that names the save, and the pill
	// has always offered a folder as "<folder>.zip".
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="saves.zip"` {
		t.Fatalf("Content-Disposition = %q, want the folder-named zip", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("the streamed body is not a zip: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != strings.TrimPrefix(fakeSavePth, "/") {
		t.Fatalf("zip holds %d entries, want just the bound path", len(zr.File))
	}
	// Without a token the GET zip route has nothing to authorize.
	if bare := do(t, e.h, http.MethodGet, "/api/v1/servers/"+e.server+"/files/download", e.token, nil); bare.Code != http.StatusBadRequest {
		t.Fatalf("GET zip with a session and no token: got %d, want 400", bare.Code)
	}
}

// Expiry is checked at redemption, not merely promised at mint.
func TestDownloadTokenExpiredIsRejected(t *testing.T) {
	e := newDownloadEnv(t)
	tok, _ := e.mint(t, e.token, e.server, map[string]any{"path": fakeCfgPath})
	e.srv.ExpireDownloadTokensForTest()
	if rec := do(t, e.h, http.MethodGet, tok.URL, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired redeem: got %d, want 401", rec.Code)
	}
}

// A token is bound to one server: it does not travel to another, even one the
// minting user also owns.
func TestDownloadTokenIsBoundToItsServer(t *testing.T) {
	e := newDownloadEnv(t)
	specID := createSpecWithBackup(t, e.h, e.token, "download-token-spec-2", nil)
	other := createServerFromSpec(t, e.h, e.token, specID, "abyssal-02")
	waitInstalled(t, e.st, other)

	tok, _ := e.mint(t, e.token, e.server, map[string]any{"path": fakeCfgPath})
	moved := strings.Replace(tok.URL, e.server, other, 1)
	if moved == tok.URL {
		t.Fatal("could not retarget the minted url at the other server")
	}
	if rec := do(t, e.h, http.MethodGet, moved, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token from server X on server Y: got %d, want 401", rec.Code)
	}
	// And a bogus token on the right server is no better.
	if rec := do(t, e.h, http.MethodGet, "/api/v1/servers/"+e.server+"/files/raw?path="+fakeCfgPath+"&token=deadbeef", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: got %d, want 401", rec.Code)
	}
}

// The token authorizes one exact path. Asking for a different or wider one with
// it is a rejection, not a widening — and a raw token is not a zip token.
func TestDownloadTokenRejectsPathAndRouteWidening(t *testing.T) {
	e := newDownloadEnv(t)

	tok, _ := e.mint(t, e.token, e.server, map[string]any{"path": fakeCfgPath})
	widened := "/api/v1/servers/" + e.server + "/files/raw?path=/data/saves/world.sav&token=" + tok.Token
	if rec := do(t, e.h, http.MethodGet, widened, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("redeem at another path: got %d, want 401", rec.Code)
	}

	// A raw token on the zip route is a route mismatch.
	tok2, _ := e.mint(t, e.token, e.server, map[string]any{"path": fakeCfgPath})
	crossed := "/api/v1/servers/" + e.server + "/files/download?token=" + tok2.Token
	if rec := do(t, e.h, http.MethodGet, crossed, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("raw token on the zip route: got %d, want 401", rec.Code)
	}

	// A spelling of the same path still redeems — canonicalization, not luck.
	tok3, _ := e.mint(t, e.token, e.server, map[string]any{"path": fakeCfgPath})
	same := "/api/v1/servers/" + e.server + "/files/raw?path=/data/saves/../server.cfg&token=" + tok3.Token
	if rec := do(t, e.h, http.MethodGet, same, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("redeem at an equivalent spelling: got %d, want 200", rec.Code)
	}
}

// The minting user's standing is re-checked at redemption: 60 seconds is long
// enough for an admin to revoke a role or delete the account outright.
func TestDownloadTokenRevalidatesTheMintingUser(t *testing.T) {
	ctx := context.Background()
	// Operator holds server.files.read but not server.any, so it is scoped to
	// servers it owns — hand it this one.
	seedOperator := func(t *testing.T, e *downloadEnv, id string) string {
		t.Helper()
		if err := e.st.CreateUser(ctx, &store.User{ID: id, Username: id, RoleID: rbac.RoleOperator, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("create user: %v", err)
		}
		if err := e.st.CreateSession(ctx, &store.Session{Token: id + "-token", UserID: id, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatalf("create session: %v", err)
		}
		sv, err := e.st.GetServer(ctx, e.server)
		if err != nil {
			t.Fatalf("get server: %v", err)
		}
		sv.OwnerID = id
		if err := e.st.UpdateServer(ctx, sv); err != nil {
			t.Fatalf("update server owner: %v", err)
		}
		return id + "-token"
	}

	t.Run("permission revoked", func(t *testing.T) {
		e := newDownloadEnv(t)
		sess := seedOperator(t, e, "op1")
		tok, code := e.mint(t, sess, e.server, map[string]any{"path": fakeCfgPath})
		if code != http.StatusCreated {
			t.Fatalf("operator mint: got %d, want 201", code)
		}
		u, err := e.st.GetUser(ctx, "op1")
		if err != nil {
			t.Fatalf("get user: %v", err)
		}
		u.RoleID = rbac.RoleReadOnly // no server.files.read any more
		if err := e.st.UpdateUser(ctx, u); err != nil {
			t.Fatalf("update user: %v", err)
		}
		if rec := do(t, e.h, http.MethodGet, tok.URL, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("redeem after revocation: got %d, want 401", rec.Code)
		}
	})

	t.Run("user deleted", func(t *testing.T) {
		e := newDownloadEnv(t)
		sess := seedOperator(t, e, "op2")
		tok, code := e.mint(t, sess, e.server, map[string]any{"path": fakeCfgPath})
		if code != http.StatusCreated {
			t.Fatalf("operator mint: got %d, want 201", code)
		}
		if err := e.st.DeleteUser(ctx, "op2"); err != nil {
			t.Fatalf("delete user: %v", err)
		}
		if rec := do(t, e.h, http.MethodGet, tok.URL, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("redeem after the user was deleted: got %d, want 401", rec.Code)
		}
	})
}

// Without ?token= the raw route is exactly what it always was: Bearer or 401.
func TestFilesRawWithoutTokenIsUnchanged(t *testing.T) {
	e := newDownloadEnv(t)
	url := "/api/v1/servers/" + e.server + "/files/raw?path=" + fakeCfgPath
	rec := do(t, e.h, http.MethodGet, url, e.token, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != fakeCfgBody {
		t.Fatalf("bearer download: got %d, body %q", rec.Code, rec.Body.String())
	}
	if anon := do(t, e.h, http.MethodGet, url, "", nil); anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous download: got %d, want 401", anon.Code)
	}
}

// Both halves of the lifecycle are on the record, with what they covered and
// never the token itself.
func TestDownloadTokenAuditsMintAndRedemption(t *testing.T) {
	e := newDownloadEnv(t)
	tok, _ := e.mint(t, e.token, e.server, map[string]any{"paths": []string{fakeCfgPath, fakeSavePth}})
	if rec := do(t, e.h, http.MethodGet, tok.URL, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("redeem: got %d, body %s", rec.Code, rec.Body.String())
	}

	rec := do(t, e.h, http.MethodGet, "/api/v1/audit", e.token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: got %d", rec.Code)
	}
	var out struct {
		Entries []store.AuditEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	var mint, redeem *store.AuditEntry
	for i := range out.Entries {
		ent := &out.Entries[i]
		if strings.Contains(ent.Action, "download token") && strings.Contains(ent.Action, "token") {
			if strings.Contains(ent.Action, "mint") {
				mint = ent
			}
			if strings.Contains(ent.Action, "redeemed") {
				redeem = ent
			}
		}
		if strings.Contains(ent.Action, tok.Token) || strings.Contains(ent.Path, tok.Token) {
			t.Fatalf("the audit log carries the token itself: %+v", ent)
		}
	}
	if mint == nil {
		t.Fatal("no audit entry for the mint")
	}
	if redeem == nil {
		t.Fatal("no audit entry for the redemption")
	}
	for _, ent := range []*store.AuditEntry{mint, redeem} {
		if ent.Actor != testAdmin {
			t.Errorf("audit actor = %q, want %q", ent.Actor, testAdmin)
		}
		if ent.TargetID != e.server {
			t.Errorf("audit target = %q, want the server id %q", ent.TargetID, e.server)
		}
		if !strings.Contains(ent.Action, "2 paths") {
			t.Errorf("audit action %q does not say how many paths it covered", ent.Action)
		}
	}
	// The mint is one row, not two: the enriched entry replaces the generic one.
	mints := 0
	for _, ent := range out.Entries {
		if ent.Method == http.MethodPost && strings.HasSuffix(ent.Path, "/files/download-token") {
			mints++
		}
	}
	if mints != 1 {
		t.Fatalf("the mint wrote %d audit rows, want 1", mints)
	}
}
