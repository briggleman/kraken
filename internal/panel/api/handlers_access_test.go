package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

// accessEnv is one server, owned by an Operator, and the people around it.
type accessEnv struct {
	h     http.Handler
	st    store.Store
	admin string // the bootstrap owner's session
}

func newAccessEnv(t *testing.T) *accessEnv {
	t.Helper()
	h, st := newTestServerStore(t)
	e := &accessEnv{h: h, st: st, admin: login(t, h)}
	for id, role := range map[string]string{
		"olga": rbac.RoleOperator, // owns srv-a
		"otto": rbac.RoleOperator,
		"ron":  rbac.RoleReadOnly,
		"sam":  rbac.RoleReadOnly, // never granted anything
	} {
		e.user(t, id, role)
	}
	if err := st.CreateServer(context.Background(), &store.Server{
		ID: "srv-a", Name: "srv-a", OwnerID: "olga", NodeID: "node-that-is-gone",
		State: store.StateInstalling, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return e
}

func (e *accessEnv) user(t *testing.T, id, role string) {
	t.Helper()
	ctx := context.Background()
	if err := e.st.CreateUser(ctx, &store.User{ID: id, Username: id, RoleID: role, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
	if err := e.st.CreateSession(ctx, &store.Session{Token: id + "-token", UserID: id, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("create session %s: %v", id, err)
	}
}

func (e *accessEnv) do(t *testing.T, who, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	tok := who + "-token"
	if who == "admin" {
		tok = e.admin
	}
	return do(t, e.h, method, path, tok, body)
}

type accessView struct {
	Owner *struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"owner"`
	Access      string `json:"access"`
	CanManage   bool   `json:"can_manage"`
	CanTransfer bool   `json:"can_transfer"`
	Grants      []struct {
		ID        string `json:"id"`
		GrantedBy string `json:"granted_by"`
	} `json:"grants"`
	Candidates []struct {
		ID string `json:"id"`
	} `json:"candidates"`
}

func decodeAccess(t *testing.T, rec *httptest.ResponseRecorder) accessView {
	t.Helper()
	var v accessView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode access view: %v (%s)", err, rec.Body.String())
	}
	return v
}

func listIDs(t *testing.T, e *accessEnv, who string) []string {
	t.Helper()
	rec := e.do(t, who, http.MethodGet, "/api/v1/servers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s list: got %d", who, rec.Code)
	}
	var out struct {
		Servers []struct {
			ID     string `json:"id"`
			Access string `json:"access"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	ids := []string{}
	for _, s := range out.Servers {
		ids = append(ids, s.ID+":"+s.Access)
	}
	return ids
}

func expect(t *testing.T, what string, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: got %d, want %d (%s)", what, rec.Code, want, rec.Body.String())
	}
}

// A grant makes a server reachable and nothing more (#146): the grantee's role
// still decides every action, a stranger still gets 404, and revoking ends it.
func TestServerGrantReachAndRole(t *testing.T) {
	e := newAccessEnv(t)

	// Before any grant, Read-only ron sees nothing — the empty list of #146.
	expect(t, "ron GET srv-a before the grant", e.do(t, "ron", http.MethodGet, "/api/v1/servers/srv-a", nil), http.StatusNotFound)
	if ids := listIDs(t, e, "ron"); len(ids) != 0 {
		t.Fatalf("ron list before the grant = %v, want empty", ids)
	}

	// The owner shares it with ron (Read-only) and otto (Operator).
	expect(t, "olga grants ron", e.do(t, "olga", http.MethodPut, "/api/v1/servers/srv-a/access/ron", nil), http.StatusOK)
	expect(t, "olga grants otto", e.do(t, "olga", http.MethodPut, "/api/v1/servers/srv-a/access/otto", nil), http.StatusOK)
	expect(t, "olga grants ron again", e.do(t, "olga", http.MethodPut, "/api/v1/servers/srv-a/access/ron", nil), http.StatusOK)

	expect(t, "ron GET srv-a", e.do(t, "ron", http.MethodGet, "/api/v1/servers/srv-a", nil), http.StatusOK)
	if ids := listIDs(t, e, "ron"); len(ids) != 1 || ids[0] != "srv-a:grant" {
		t.Fatalf("ron list = %v, want [srv-a:grant]", ids)
	}
	if ids := listIDs(t, e, "olga"); len(ids) != 1 || ids[0] != "srv-a:owner" {
		t.Fatalf("olga list = %v, want [srv-a:owner]", ids)
	}
	if ids := listIDs(t, e, "admin"); len(ids) != 1 || ids[0] != "srv-a:any" {
		t.Fatalf("admin list = %v, want [srv-a:any]", ids)
	}
	expect(t, "sam GET srv-a", e.do(t, "sam", http.MethodGet, "/api/v1/servers/srv-a", nil), http.StatusNotFound)

	// The role still decides: Read-only cannot power, an Operator grantee gets
	// past every gate to the handler (which refuses an installing server).
	expect(t, "ron power", e.do(t, "ron", http.MethodPost, "/api/v1/servers/srv-a/power", map[string]string{"action": "start"}), http.StatusForbidden)
	if rec := e.do(t, "otto", http.MethodPost, "/api/v1/servers/srv-a/power", map[string]string{"action": "start"}); rec.Code == http.StatusNotFound || rec.Code == http.StatusForbidden {
		t.Fatalf("otto power on a granted server: got %d, want past the access and permission gates", rec.Code)
	}

	// The console stream applies the same rule (#422).
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	dial := func(who string) (int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/servers/srv-a/stream/ws"
		c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"kraken.token", who + "-token"}})
		if err != nil {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			return code, err
		}
		c.CloseNow()
		return http.StatusSwitchingProtocols, nil
	}
	if code, err := dial("ron"); err != nil {
		t.Fatalf("ron's console stream: HTTP %d, %v", code, err)
	}
	if code, _ := dial("sam"); code != http.StatusNotFound {
		t.Fatalf("sam's console stream: HTTP %d, want 404", code)
	}

	// Revoking ends it; revoking again is a 404.
	expect(t, "olga revokes ron", e.do(t, "olga", http.MethodDelete, "/api/v1/servers/srv-a/access/ron", nil), http.StatusOK)
	expect(t, "ron GET srv-a after the revoke", e.do(t, "ron", http.MethodGet, "/api/v1/servers/srv-a", nil), http.StatusNotFound)
	expect(t, "olga revokes ron again", e.do(t, "olga", http.MethodDelete, "/api/v1/servers/srv-a/access/ron", nil), http.StatusNotFound)
}

// Who may manage access: the owner and an administrator. A grantee may look
// but not share; a stranger cannot tell the server exists.
func TestServerAccessManageRights(t *testing.T) {
	e := newAccessEnv(t)
	expect(t, "olga grants ron", e.do(t, "olga", http.MethodPut, "/api/v1/servers/srv-a/access/ron", nil), http.StatusOK)

	// The owner's view: grants and candidates, no transfer.
	v := decodeAccess(t, e.do(t, "olga", http.MethodGet, "/api/v1/servers/srv-a/access", nil))
	if !v.CanManage || v.CanTransfer || v.Access != "owner" || v.Owner == nil || v.Owner.ID != "olga" {
		t.Fatalf("olga's view = %+v, want owner who can manage but not transfer", v)
	}
	if len(v.Grants) != 1 || v.Grants[0].ID != "ron" || v.Grants[0].GrantedBy != "olga" {
		t.Fatalf("olga's grants = %+v, want ron granted by olga", v.Grants)
	}
	cands := map[string]bool{}
	for _, c := range v.Candidates {
		cands[c.ID] = true
	}
	// otto and sam can be granted; olga owns it, ron has it, and the bootstrap
	// admin's role reaches every server anyway.
	if !cands["otto"] || !cands["sam"] || cands["olga"] || cands["ron"] || len(cands) != 2 {
		t.Fatalf("candidates = %v, want exactly otto and sam", cands)
	}

	// A grantee sees the owner and nothing to manage.
	v = decodeAccess(t, e.do(t, "ron", http.MethodGet, "/api/v1/servers/srv-a/access", nil))
	if v.CanManage || v.Access != "grant" || v.Owner == nil || v.Owner.ID != "olga" || v.Grants != nil || v.Candidates != nil {
		t.Fatalf("ron's view = %+v, want the owner and no grants or candidates", v)
	}
	expect(t, "ron grants sam", e.do(t, "ron", http.MethodPut, "/api/v1/servers/srv-a/access/sam", nil), http.StatusForbidden)
	expect(t, "ron revokes himself", e.do(t, "ron", http.MethodDelete, "/api/v1/servers/srv-a/access/ron", nil), http.StatusForbidden)

	// A stranger gets 404 everywhere, never 403.
	expect(t, "sam reads access", e.do(t, "sam", http.MethodGet, "/api/v1/servers/srv-a/access", nil), http.StatusNotFound)
	expect(t, "sam grants himself", e.do(t, "sam", http.MethodPut, "/api/v1/servers/srv-a/access/sam", nil), http.StatusNotFound)

	// An administrator manages any server.
	expect(t, "admin grants sam", e.do(t, "admin", http.MethodPut, "/api/v1/servers/srv-a/access/sam", nil), http.StatusOK)
	expect(t, "sam GET srv-a", e.do(t, "sam", http.MethodGet, "/api/v1/servers/srv-a", nil), http.StatusOK)

	// Granting the owner, or nobody, is refused.
	expect(t, "grant the owner", e.do(t, "admin", http.MethodPut, "/api/v1/servers/srv-a/access/olga", nil), http.StatusConflict)
	expect(t, "grant an unknown user", e.do(t, "admin", http.MethodPut, "/api/v1/servers/srv-a/access/nobody", nil), http.StatusNotFound)
}

// Only an administrator moves ownership. The new owner's grant goes, the old
// owner keeps nothing they are not granted, and a deleted owner leaves a
// server only administrators (and grantees) reach until it is transferred.
func TestServerTransfer(t *testing.T) {
	e := newAccessEnv(t)
	expect(t, "olga grants otto", e.do(t, "olga", http.MethodPut, "/api/v1/servers/srv-a/access/otto", nil), http.StatusOK)

	expect(t, "olga transfers", e.do(t, "olga", http.MethodPut, "/api/v1/servers/srv-a/owner", map[string]string{"user_id": "otto"}), http.StatusForbidden)
	expect(t, "transfer to nobody", e.do(t, "admin", http.MethodPut, "/api/v1/servers/srv-a/owner", map[string]string{"user_id": "nobody"}), http.StatusNotFound)
	expect(t, "transfer without a user", e.do(t, "admin", http.MethodPut, "/api/v1/servers/srv-a/owner", map[string]string{}), http.StatusBadRequest)

	rec := e.do(t, "admin", http.MethodPut, "/api/v1/servers/srv-a/owner", map[string]string{"user_id": "otto"})
	expect(t, "admin transfers to otto", rec, http.StatusOK)
	if v := decodeAccess(t, rec); v.Owner == nil || v.Owner.ID != "otto" || len(v.Grants) != 0 {
		t.Fatalf("view after transfer = %+v, want otto owning it with his grant gone", v)
	}
	if ids := listIDs(t, e, "otto"); len(ids) != 1 || ids[0] != "srv-a:owner" {
		t.Fatalf("otto list = %v, want [srv-a:owner]", ids)
	}
	expect(t, "olga GET srv-a after giving it away", e.do(t, "olga", http.MethodGet, "/api/v1/servers/srv-a", nil), http.StatusNotFound)

	// The owner is deleted: the server is nobody's, and a grantee still reaches it.
	expect(t, "otto grants ron", e.do(t, "otto", http.MethodPut, "/api/v1/servers/srv-a/access/ron", nil), http.StatusOK)
	expect(t, "admin deletes otto", e.do(t, "admin", http.MethodDelete, "/api/v1/users/otto", nil), http.StatusNoContent)
	v := decodeAccess(t, e.do(t, "admin", http.MethodGet, "/api/v1/servers/srv-a/access", nil))
	if v.Owner != nil {
		t.Fatalf("owner after the owner was deleted = %+v, want none", v.Owner)
	}
	expect(t, "ron GET the ownerless srv-a", e.do(t, "ron", http.MethodGet, "/api/v1/servers/srv-a", nil), http.StatusOK)

	// A deleted grantee's grant goes with them.
	expect(t, "admin deletes ron", e.do(t, "admin", http.MethodDelete, "/api/v1/users/ron", nil), http.StatusNoContent)
	if ok, _ := e.st.HasServerGrant(context.Background(), "srv-a", "ron"); ok {
		t.Fatal("ron's grant outlived ron")
	}
}
