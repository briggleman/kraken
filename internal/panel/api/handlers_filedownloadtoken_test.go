package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/config"
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

// seedDownloadOperator creates a user on the Operator role — which holds
// server.files.read but NOT server.any, so it is scoped to servers it owns —
// gives it a live session, and hands it the env's server.
func seedDownloadOperator(t *testing.T, e *downloadEnv, id string) string {
	t.Helper()
	ctx := context.Background()
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

	t.Run("permission revoked", func(t *testing.T) {
		e := newDownloadEnv(t)
		sess := seedDownloadOperator(t, e, "op1")
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
		sess := seedDownloadOperator(t, e, "op2")
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

// A grant is no stronger than the session that minted it. Logging out, or
// having a session revoked by an admin, used to leave a minted token good for
// the rest of its 60 seconds; the redemption now checks the session is still
// on record before it serves a byte.
func TestDownloadTokenDiesWithItsSession(t *testing.T) {
	ctx := context.Background()

	t.Run("live session still streams", func(t *testing.T) {
		e := newDownloadEnv(t)
		sess := seedDownloadOperator(t, e, "sess-ok")
		tok, code := e.mint(t, sess, e.server, map[string]any{"path": fakeCfgPath})
		if code != http.StatusCreated {
			t.Fatalf("mint: got %d, want 201", code)
		}
		if rec := do(t, e.h, http.MethodGet, tok.URL, "", nil); rec.Code != http.StatusOK {
			t.Fatalf("redeem on a live session: got %d, body %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("logged out", func(t *testing.T) {
		e := newDownloadEnv(t)
		sess := seedDownloadOperator(t, e, "sess-logout")
		tok, code := e.mint(t, sess, e.server, map[string]any{"path": fakeCfgPath})
		if code != http.StatusCreated {
			t.Fatalf("mint: got %d, want 201", code)
		}
		if out := do(t, e.h, http.MethodPost, "/api/v1/auth/logout", sess, nil); out.Code != http.StatusOK {
			t.Fatalf("logout: got %d", out.Code)
		}
		if rec := do(t, e.h, http.MethodGet, tok.URL, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("redeem after logout: got %d, want 401", rec.Code)
		}
	})

	t.Run("session revoked", func(t *testing.T) {
		e := newDownloadEnv(t)
		sess := seedDownloadOperator(t, e, "sess-revoked")
		tok, code := e.mint(t, sess, e.server, map[string]any{"path": fakeCfgPath})
		if code != http.StatusCreated {
			t.Fatalf("mint: got %d, want 201", code)
		}
		if err := e.st.DeleteSession(ctx, sess); err != nil {
			t.Fatalf("delete session: %v", err)
		}
		if rec := do(t, e.h, http.MethodGet, tok.URL, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("redeem after the session was revoked: got %d, want 401", rec.Code)
		}
	})
}

// The raw route announces how many bytes are coming, so the browser can check
// the transfer against a number rather than trusting a clean end of stream —
// and says outright that a resume is not on offer, because a single-use token
// cannot serve one. A zip announces nothing: its size is not known until the
// Agent has written it.
func TestDownloadAnnouncesLengthAndRefusesRanges(t *testing.T) {
	e := newDownloadEnv(t)

	raw := do(t, e.h, http.MethodGet,
		"/api/v1/servers/"+e.server+"/files/raw?path="+fakeCfgPath, e.token, nil)
	if raw.Code != http.StatusOK {
		t.Fatalf("raw download: got %d, body %s", raw.Code, raw.Body.String())
	}
	if cl := raw.Header().Get("Content-Length"); cl != strconv.Itoa(len(fakeCfgBody)) {
		t.Fatalf("Content-Length = %q, want the file's %d bytes", cl, len(fakeCfgBody))
	}
	if ar := raw.Header().Get("Accept-Ranges"); ar != "none" {
		t.Fatalf("Accept-Ranges = %q, want none", ar)
	}

	tok, _ := e.mint(t, e.token, e.server, map[string]any{"paths": []string{fakeSavePth}})
	zipped := do(t, e.h, http.MethodGet, tok.URL, "", nil)
	if zipped.Code != http.StatusOK {
		t.Fatalf("zip download: got %d, body %s", zipped.Code, zipped.Body.String())
	}
	if cl := zipped.Header().Get("Content-Length"); cl != "" {
		t.Fatalf("a zip announced Content-Length %q, but its size is not known until it is written", cl)
	}
}

// The redemption routes take no credentials, so they are rate limited per
// source IP. The refusal is the ordinary JSON envelope plus a Retry-After.
func TestDownloadRedemptionIsRateLimited(t *testing.T) {
	e := newDownloadEnv(t)
	url := "/api/v1/servers/" + e.server + "/files/raw?path=" + fakeCfgPath + "&token=deadbeef"
	var last *httptest.ResponseRecorder
	for range api.DownloadRedeemBurstForTest + 1 {
		last = do(t, e.h, http.MethodGet, url, "", nil)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("request past the burst: got %d, want 429", last.Code)
	}
	if ra := last.Header().Get("Retry-After"); ra == "" {
		t.Fatal("the 429 carried no Retry-After")
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(last.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Fatalf("429 body = %q, want a JSON error envelope", last.Body.String())
	}
}

// The login limiter is a ceiling on scripted guessing, not on operators: a
// normal sign-in sequence — and a fumbled password before it — goes through
// untouched.
func TestLoginRateLimitLeavesANormalSignInAlone(t *testing.T) {
	h := newTestServer(t)
	for range 3 {
		if rec := do(t, h, http.MethodPost, "/api/v1/auth/login", "",
			map[string]string{"username": testAdmin, "password": "wrong"}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("bad password: got %d, want 401", rec.Code)
		}
	}
	token := login(t, h)
	if rec := do(t, h, http.MethodGet, "/api/v1/auth/me", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("me after login: got %d", rec.Code)
	}
}

// The limit is on the token branch, not the route. An operator downloading
// files with a live session shares a NAT address with whoever else is behind
// it — including somebody probing tokens — and must not be refused for their
// company. The probes are refused; the session is not.
func TestOnlyTokenRedemptionIsRateLimited(t *testing.T) {
	e := newDownloadEnv(t)

	authed := "/api/v1/servers/" + e.server + "/files/raw?path=" + fakeCfgPath
	for i := range api.DownloadRedeemBurstForTest + 1 {
		if rec := do(t, e.h, http.MethodGet, authed, e.token, nil); rec.Code != http.StatusOK {
			t.Fatalf("authenticated download %d: got %d, want 200 — the Bearer path must not be limited", i+1, rec.Code)
		}
	}

	probe := "/api/v1/servers/" + e.server + "/files/raw?path=" + fakeCfgPath + "&token=deadbeef"
	var last *httptest.ResponseRecorder
	for range api.DownloadRedeemBurstForTest + 1 {
		last = do(t, e.h, http.MethodGet, probe, "", nil)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("token probe past the burst: got %d, want 429", last.Code)
	}

	// And the session still works afterwards: the probing did not spend the
	// operator's capacity, because the operator never had a bucket.
	if rec := do(t, e.h, http.MethodGet, authed, e.token, nil); rec.Code != http.StatusOK {
		t.Fatalf("authenticated download after a probe flood: got %d, want 200", rec.Code)
	}
}

// KRAKEN_RATE_LIMITS=off is a real off switch on both limiters, for an operator
// whose edge already does this (or who is debugging the one that does).
func TestRateLimitsOffSwitch(t *testing.T) {
	h := newTestServerWith(t, func(cfg *config.Config) { cfg.RateLimits = "off" })
	for i := range 40 {
		if rec := do(t, h, http.MethodPost, "/api/v1/auth/login", "",
			map[string]string{"username": testAdmin, "password": "wrong"}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("login attempt %d: got %d, want 401 with limiting off", i+1, rec.Code)
		}
	}
	token := login(t, h)
	serverless := "/api/v1/servers/nope/files/raw?token=deadbeef"
	for i := range 40 {
		if rec := do(t, h, http.MethodGet, serverless, "", nil); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("token probe %d was rate limited although limiting is off", i+1)
		}
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/auth/me", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("me: got %d", rec.Code)
	}
}

// A brute-force flood never reaches the handler that audits a failed login, so
// the limiter leaves the row itself — once per key per window, not once per
// request, or the flood would just move into the audit table. Two limiters
// guard this route now, on different keys, so a flood that trips both leaves
// one row EACH: "this username is being guessed at" and "this address is
// hammering the door" are different findings.
func TestLoginRateLimitLeavesOneAuditRow(t *testing.T) {
	h := newTestServer(t)
	admin := login(t, h)
	for range api.LoginBurstForTest + 5 {
		do(t, h, http.MethodPost, "/api/v1/auth/login", "",
			map[string]string{"username": "nobody", "password": "guess"})
	}
	rec := do(t, h, http.MethodGet, "/api/v1/audit", admin, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: got %d", rec.Code)
	}
	var out struct {
		Entries []store.AuditEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	byKey := map[string]int{}
	for _, ent := range out.Entries {
		if ent.Status != http.StatusTooManyRequests || !strings.Contains(ent.Action, "rate limited") {
			continue
		}
		switch {
		case strings.Contains(ent.Action, "this username"):
			byKey["username"]++
		case strings.Contains(ent.Action, "this address"):
			byKey["address"]++
		default:
			t.Fatalf("unrecognized rate-limit audit action %q", ent.Action)
		}
	}
	if byKey["username"] != 1 {
		t.Fatalf("the flood left %d per-username rate-limit rows, want exactly 1", byKey["username"])
	}
	if byKey["address"] != 1 {
		t.Fatalf("the flood left %d per-address rate-limit rows, want exactly 1", byKey["address"])
	}
}

// An upload is bounded by the body reader, not by ParseMultipartForm's argument
// — that is only the in-memory threshold, and everything past it spills to temp
// files with no limit of its own. Without the cap one authenticated request
// could fill the Panel's disk.
func TestUploadBodyIsCapped(t *testing.T) {
	e := newDownloadEnv(t)

	body, contentType := oversizeUpload(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/servers/"+e.server+"/files/upload?path=/data", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+e.token)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload: got %d, want 413", rec.Code)
	}
}

// oversizeUpload streams a multipart body past the Panel's cap without ever
// holding it in memory — the point is what the server does with the bytes, not
// what the test can allocate.
func oversizeUpload(t *testing.T) (io.Reader, string) {
	t.Helper()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("files", "huge.bin")
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		// Comfortably past maxUploadBytes + the framing slack.
		if _, err := io.CopyN(part, zeroes{}, 70<<20); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = mw.Close()
		_ = pw.Close()
	}()
	return pr, mw.FormDataContentType()
}

type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) { return len(p), nil }
