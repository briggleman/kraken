package api_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// The device-registration surface of the push-alert contract (#348,
// docs/design/push-alerts.md).

const (
	deviceA = "e621e1f8-c36c-495a-93fc-0c247a3e6e5f"
	deviceB = "6f9619ff-8b86-d011-b42d-00c04fc964ff"
	tokenA  = "740f4707bebcf74f9b7c25d48e3358945f6aa01da5ddb387462c7eaf61bb78ad"
	tokenA2 = "8a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9"
)

// deviceKey is a fresh device public key, base64, as the app sends it.
func deviceKey(t *testing.T) string {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

func registration(t *testing.T, id, token string) map[string]any {
	return map[string]any{
		"id": id, "platform": "ios", "apns_token": token, "apns_environment": "production",
		"public_key": deviceKey(t), "name": "Ben's iPhone",
	}
}

type deviceResp struct {
	ID              string            `json:"id"`
	UserID          string            `json:"user_id"`
	Platform        string            `json:"platform"`
	APNsEnvironment string            `json:"apns_environment"`
	Name            string            `json:"name"`
	Rules           store.DeviceRules `json:"rules"`
	CreatedAt       time.Time         `json:"created_at"`
	LastSeenAt      time.Time         `json:"last_seen_at"`
	TokenInvalidAt  *time.Time        `json:"token_invalid_at"`
}

func decodeDevice(t *testing.T, rec *httptest.ResponseRecorder) deviceResp {
	t.Helper()
	var d deviceResp
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode device: %v (%s)", err, rec.Body.String())
	}
	return d
}

func listDevices(t *testing.T, h http.Handler, tok, query string) (int, []deviceResp) {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/devices"+query, tok, nil)
	var out struct {
		Devices []deviceResp `json:"devices"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode list: %v", err)
		}
	}
	return rec.Code, out.Devices
}

// seedUser adds a user with a live session and returns the session token.
func seedUser(t *testing.T, st *memory.Store, id, role string) string {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{ID: id, Username: id, RoleID: role, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
	tok := id + "-token"
	if err := st.CreateSession(ctx, &store.Session{Token: tok, UserID: id, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return tok
}

func adminID(t *testing.T, st *memory.Store) string {
	t.Helper()
	u, err := st.GetUserByUsername(context.Background(), testAdmin)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestRegisterDevice(t *testing.T) {
	h, st := newTestServerStore(t)
	tok := login(t, h)

	// iOS spells its UUIDs in upper case; the row is the canonical form.
	rec := do(t, h, http.MethodPost, "/api/v1/devices", tok, registration(t, strings.ToUpper(deviceA), strings.ToUpper(tokenA)))
	if rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	d := decodeDevice(t, rec)
	if d.ID != deviceA || d.UserID != adminID(t, st) || d.Platform != "ios" || d.APNsEnvironment != "production" || d.Name != "Ben's iPhone" {
		t.Fatalf("registered device = %+v", d)
	}
	want := store.DeviceRules{Attend: true, Healed: true, Alive: true, AliveMutedServers: []string{}}
	if d.Rules.Attend != want.Attend || d.Rules.Healed != want.Healed || d.Rules.Alive != want.Alive ||
		d.Rules.AliveMutedServers == nil || len(d.Rules.AliveMutedServers) != 0 {
		t.Fatalf("a new device's rules = %+v, want every class on and nothing muted", d.Rules)
	}
	// The token and the key are the phone's; the answer carries neither.
	body := strings.ToLower(rec.Body.String())
	if strings.Contains(body, tokenA) || strings.Contains(body, "apns_token") || strings.Contains(body, "public_key") {
		t.Fatalf("the answer carries the token or the key: %s", rec.Body.String())
	}
	stored, err := st.GetDevice(context.Background(), adminID(t, st), deviceA)
	if err != nil || stored.APNsToken != tokenA || len(stored.PublicKey) != 32 {
		t.Fatalf("stored device = %+v err=%v; want the lowercase token and a 32-byte key", stored, err)
	}
}

// A refresh with a rotated token is the same row with the new token: the
// device's rules and creation time stay, last_seen_at moves, and a token the
// relay had reported dead is forgiven.
func TestRegisterDeviceRefreshRotatesToken(t *testing.T) {
	h, st := newTestServerStore(t)
	tok := login(t, h)
	ctx := context.Background()

	first := decodeDevice(t, do(t, h, http.MethodPost, "/api/v1/devices", tok, registration(t, deviceA, tokenA)))
	if rec := do(t, h, http.MethodPatch, "/api/v1/devices/"+deviceA+"/rules", tok, map[string]any{"healed": false}); rec.Code != http.StatusOK {
		t.Fatalf("patch rules: %d %s", rec.Code, rec.Body.String())
	}
	if ok, err := st.MarkDeviceTokenInvalid(ctx, adminID(t, st), deviceA, tokenA, time.Now()); !ok || err != nil {
		t.Fatalf("mark invalid: %v %v", ok, err)
	}
	time.Sleep(2 * time.Millisecond)

	reg := registration(t, deviceA, tokenA2)
	reg["apns_environment"] = "sandbox"
	rec := do(t, h, http.MethodPost, "/api/v1/devices", tok, reg)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	again := decodeDevice(t, rec)
	if again.Rules.Healed || !again.Rules.Attend || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("refresh lost the device's rules or creation time: %+v", again)
	}
	if !again.LastSeenAt.After(first.LastSeenAt) {
		t.Fatalf("last_seen_at did not move: %v then %v", first.LastSeenAt, again.LastSeenAt)
	}
	if again.TokenInvalidAt != nil || again.APNsEnvironment != "sandbox" {
		t.Fatalf("refresh = %+v; want the dead-token mark cleared and the new environment", again)
	}
	if _, all := listDevices(t, h, tok, ""); len(all) != 1 {
		t.Fatalf("devices after a refresh = %d, want the one row", len(all))
	}
	stored, _ := st.GetDevice(ctx, adminID(t, st), deviceA)
	if stored.APNsToken != tokenA2 {
		t.Fatal("the rotated token was not stored")
	}
}

func TestRegisterDeviceRejectsWhatTheContractDoesNot(t *testing.T) {
	h := newTestServer(t)
	tok := login(t, h)
	zero := base64.StdEncoding.EncodeToString(make([]byte, 32))
	short := base64.StdEncoding.EncodeToString(make([]byte, 31))

	cases := map[string]func(m map[string]any){
		"id not a uuid":        func(m map[string]any) { m["id"] = "my-phone" },
		"nil uuid":             func(m map[string]any) { m["id"] = "00000000-0000-0000-0000-000000000000" },
		"platform android":     func(m map[string]any) { m["platform"] = "android" },
		"no platform":          func(m map[string]any) { delete(m, "platform") },
		"environment dev":      func(m map[string]any) { m["apns_environment"] = "development" },
		"token not hex":        func(m map[string]any) { m["apns_token"] = strings.Repeat("zz", 32) },
		"token too short":      func(m map[string]any) { m["apns_token"] = tokenA[:40] },
		"token too long":       func(m map[string]any) { m["apns_token"] = strings.Repeat("ab", 101) },
		"key not base64":       func(m map[string]any) { m["public_key"] = "not base64!" },
		"key 31 bytes":         func(m map[string]any) { m["public_key"] = short },
		"key low-order point":  func(m map[string]any) { m["public_key"] = zero },
		"name too long":        func(m map[string]any) { m["name"] = strings.Repeat("x", 101) },
		"name control chars":   func(m map[string]any) { m["name"] = "phone\x00" },
		"key as base64url raw": func(m map[string]any) { m["public_key"] = "____________________________________________" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := registration(t, deviceA, tokenA)
			mutate(m)
			rec := do(t, h, http.MethodPost, "/api/v1/devices", tok, m)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: got %d %s, want 400", name, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), tokenA) {
				t.Fatalf("%s: the refusal echoes the token", name)
			}
		})
	}
	if _, all := listDevices(t, h, tok, ""); len(all) != 0 {
		t.Fatalf("a refused registration left %d devices", len(all))
	}
}

// The app ships through the App Store on its own schedule while each Panel is
// upgraded whenever its operator gets to it, so a field a newer app sends must
// not cost it its registration or its rules on an older Panel.
func TestDeviceBodiesIgnoreUnknownFields(t *testing.T) {
	h := newTestServer(t)
	tok := login(t, h)
	reg := registration(t, deviceA, tokenA)
	reg["push_to_talk"] = true
	if rec := do(t, h, http.MethodPost, "/api/v1/devices", tok, reg); rec.Code != http.StatusOK {
		t.Fatalf("register with an unknown field: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, http.MethodPatch, "/api/v1/devices/"+deviceA+"/rules", tok, map[string]any{"alive": false, "maintenance": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch with an unknown field: %d %s", rec.Code, rec.Body.String())
	}
	if d := decodeDevice(t, rec); d.Rules.Alive {
		t.Fatalf("the known field was not applied: %+v", d.Rules)
	}
}

// A device is its user and its id together. Device ids are not secret, so a
// second account registering the same id — the same install signed into two
// accounts, or anyone who learned the id — gets a row of its own and leaves
// the first user's row, token and rules exactly as they were.
func TestRegisterSameIDForAnotherUserLeavesTheFirstAlone(t *testing.T) {
	h, st := newTestServerStore(t)
	ctx := context.Background()
	adminTok := login(t, h)
	bobTok := seedUser(t, st, "bob", rbac.RoleReadOnly)

	do(t, h, http.MethodPost, "/api/v1/devices", bobTok, registration(t, deviceA, tokenA))
	if rec := do(t, h, http.MethodPatch, "/api/v1/devices/"+deviceA+"/rules", bobTok, map[string]any{"alive": false}); rec.Code != http.StatusOK {
		t.Fatalf("bob patch: %d %s", rec.Code, rec.Body.String())
	}
	before, err := st.GetDevice(ctx, "bob", deviceA)
	if err != nil {
		t.Fatal(err)
	}

	rec := do(t, h, http.MethodPost, "/api/v1/devices", adminTok, registration(t, deviceA, tokenA2))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin registering the same id: %d %s", rec.Code, rec.Body.String())
	}
	if d := decodeDevice(t, rec); d.UserID != adminID(t, st) || !d.Rules.Alive {
		t.Fatalf("the admin's row = %+v; want their own, with default rules", d)
	}
	after, err := st.GetDevice(ctx, "bob", deviceA)
	if err != nil {
		t.Fatalf("bob's row is gone: %v", err)
	}
	if after.APNsToken != tokenA || after.Rules.Alive || !after.LastSeenAt.Equal(before.LastSeenAt) ||
		!after.CreatedAt.Equal(before.CreatedAt) || string(after.PublicKey) != string(before.PublicKey) {
		t.Fatalf("bob's row changed: before %+v, after %+v", before, after)
	}
	if _, bobs := listDevices(t, h, bobTok, ""); len(bobs) != 1 {
		t.Fatalf("bob lists %d devices, want his one", len(bobs))
	}
	if _, admins := listDevices(t, h, adminTok, ""); len(admins) != 1 {
		t.Fatalf("the admin lists %d devices, want their one", len(admins))
	}
	// Each user's routes reach only their own row with that id.
	if rec := do(t, h, http.MethodDelete, "/api/v1/devices/"+deviceA, adminTok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("admin revoking their own: %d", rec.Code)
	}
	if _, err := st.GetDevice(ctx, "bob", deviceA); err != nil {
		t.Fatal("revoking the admin's row took bob's")
	}
}

func TestListDevicesOwnAndOtherUsers(t *testing.T) {
	h, st := newTestServerStore(t)
	adminTok := login(t, h)
	bobTok := seedUser(t, st, "bob", rbac.RoleReadOnly)
	admin := adminID(t, st)

	do(t, h, http.MethodPost, "/api/v1/devices", adminTok, registration(t, deviceA, tokenA))
	do(t, h, http.MethodPost, "/api/v1/devices", bobTok, registration(t, deviceB, tokenA2))

	if code, own := listDevices(t, h, bobTok, ""); code != http.StatusOK || len(own) != 1 || own[0].ID != deviceB {
		t.Fatalf("bob's own list: %d %+v", code, own)
	}
	if code, own := listDevices(t, h, bobTok, "?user=bob"); code != http.StatusOK || len(own) != 1 {
		t.Fatalf("bob naming himself: %d %+v", code, own)
	}
	if code, _ := listDevices(t, h, bobTok, "?user="+admin); code != http.StatusForbidden {
		t.Fatalf("bob listing the admin's devices: %d, want 403", code)
	}
	if code, bobs := listDevices(t, h, adminTok, "?user=bob"); code != http.StatusOK || len(bobs) != 1 || bobs[0].ID != deviceB {
		t.Fatalf("admin listing bob's: %d %+v", code, bobs)
	}
	if code, _ := listDevices(t, h, adminTok, "?user=nobody"); code != http.StatusNotFound {
		t.Fatalf("admin listing an unknown user's: %d, want 404", code)
	}
}

func TestPatchDeviceRules(t *testing.T) {
	h, st := newTestServerStore(t)
	adminTok := login(t, h)
	bobTok := seedUser(t, st, "bob", rbac.RoleReadOnly)
	ctx := context.Background()
	for id, owner := range map[string]string{"srv-bob": "bob", "srv-other": "someone-else"} {
		if err := st.CreateServer(ctx, &store.Server{ID: id, Name: id, OwnerID: owner, NodeID: "n1", State: store.StateOffline, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	do(t, h, http.MethodPost, "/api/v1/devices", bobTok, registration(t, deviceB, tokenA))
	path := "/api/v1/devices/" + deviceB + "/rules"

	rec := do(t, h, http.MethodPatch, path, bobTok, map[string]any{"alive": false, "alive_muted_servers": []string{"srv-bob", "srv-bob"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	d := decodeDevice(t, rec)
	if d.Rules.Alive || !d.Rules.Attend || !d.Rules.Healed || len(d.Rules.AliveMutedServers) != 1 || d.Rules.AliveMutedServers[0] != "srv-bob" {
		t.Fatalf("rules after patch = %+v", d.Rules)
	}

	// A server bob cannot view, and one that does not exist, are refused alike.
	other := do(t, h, http.MethodPatch, path, bobTok, map[string]any{"alive_muted_servers": []string{"srv-other"}})
	missing := do(t, h, http.MethodPatch, path, bobTok, map[string]any{"alive_muted_servers": []string{"srv-missing"}})
	if other.Code != http.StatusBadRequest || missing.Code != http.StatusBadRequest {
		t.Fatalf("muting a server bob cannot view: %d, a missing one: %d; want 400 both", other.Code, missing.Code)
	}
	if strings.Replace(other.Body.String(), "srv-other", "X", 1) != strings.Replace(missing.Body.String(), "srv-missing", "X", 1) {
		t.Fatalf("the refusals tell an invisible server from a missing one: %s / %s", other.Body.String(), missing.Body.String())
	}

	// Fields left out keep their value; an empty list unmutes everything.
	rec = do(t, h, http.MethodPatch, path, bobTok, map[string]any{"alive_muted_servers": []string{}})
	if d = decodeDevice(t, rec); d.Rules.Alive || len(d.Rules.AliveMutedServers) != 0 {
		t.Fatalf("rules after clearing the mutes = %+v", d.Rules)
	}

	// The route reaches only the caller's own row: an administrator has none
	// with this id, and there is no way to name bob's.
	if rec := do(t, h, http.MethodPatch, path, adminTok, map[string]any{"attend": false}); rec.Code != http.StatusNotFound {
		t.Fatalf("admin patching bob's device: %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodPatch, path+"?user=bob", adminTok, map[string]any{"attend": false}); rec.Code != http.StatusNotFound {
		t.Fatalf("admin patching bob's device by naming him: %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodPatch, "/api/v1/devices/"+deviceA+"/rules", bobTok, map[string]any{"attend": false}); rec.Code != http.StatusNotFound {
		t.Fatalf("patching an unknown device: %d, want 404", rec.Code)
	}
}

func TestDeleteDevice(t *testing.T) {
	h, st := newTestServerStore(t)
	adminTok := login(t, h)
	bobTok := seedUser(t, st, "bob", rbac.RoleReadOnly)

	do(t, h, http.MethodPost, "/api/v1/devices", adminTok, registration(t, deviceA, tokenA))
	do(t, h, http.MethodPost, "/api/v1/devices", bobTok, registration(t, deviceB, tokenA2))

	admin := adminID(t, st)
	// Bob's own row with the admin's id does not exist, and naming the admin
	// takes user.manage.
	if rec := do(t, h, http.MethodDelete, "/api/v1/devices/"+deviceA, bobTok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bob revoking the admin's device id as his own: %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/devices/"+deviceA+"?user="+admin, bobTok, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("bob revoking the admin's device: %d, want 403", rec.Code)
	}
	if _, err := st.GetDevice(context.Background(), admin, deviceA); err != nil {
		t.Fatal("the admin's device went")
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/devices/"+strings.ToUpper(deviceB), bobTok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("bob revoking his own: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/devices/"+deviceB, bobTok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("revoking it twice: %d, want 404", rec.Code)
	}
	do(t, h, http.MethodPost, "/api/v1/devices", bobTok, registration(t, deviceB, tokenA2))
	if rec := do(t, h, http.MethodDelete, "/api/v1/devices/"+deviceB, adminTok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("an admin revoking bob's without naming him: %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/devices/"+deviceB+"?user=bob", adminTok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("an admin revoking bob's: %d", rec.Code)
	}
	if _, err := st.GetDevice(context.Background(), "bob", deviceB); err == nil {
		t.Fatal("bob's device survived the admin's revoke")
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/devices/"+deviceB+"?user=nobody", adminTok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("naming an unknown user: %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/devices/not-a-uuid", adminTok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a malformed id: %d, want 404", rec.Code)
	}
}

// Sign-out revokes the user's own registration of the install it names — never
// another user's registration of the same install — and never fails for what
// the body says.
func TestLogoutRevokesTheDevice(t *testing.T) {
	h, st := newTestServerStore(t)
	ctx := context.Background()
	bobTok := seedUser(t, st, "bob", rbac.RoleReadOnly)
	adminTok := login(t, h)
	admin := adminID(t, st)
	do(t, h, http.MethodPost, "/api/v1/devices", adminTok, registration(t, deviceA, tokenA))
	do(t, h, http.MethodPost, "/api/v1/devices", bobTok, registration(t, deviceB, tokenA2))
	// The same install signed into bob's account too.
	do(t, h, http.MethodPost, "/api/v1/devices", bobTok, registration(t, deviceA, tokenA))

	// Bob signs out of that install: his registration goes, the admin's stays.
	if rec := do(t, h, http.MethodPost, "/api/v1/auth/logout", bobTok, map[string]string{"device_id": deviceA}); rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	if _, err := st.GetDevice(ctx, "bob", deviceA); err == nil {
		t.Fatal("bob's registration of the install he signed out of is still there")
	}
	if _, err := st.GetDevice(ctx, admin, deviceA); err != nil {
		t.Fatal("bob's sign-out revoked the admin's registration")
	}
	if _, err := st.GetSession(ctx, bobTok); err == nil {
		t.Fatal("bob's session survived his sign-out")
	}

	// The admin names their own: it goes with the session.
	if rec := do(t, h, http.MethodPost, "/api/v1/auth/logout", adminTok, map[string]string{"device_id": strings.ToUpper(deviceA)}); rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	if _, err := st.GetDevice(ctx, admin, deviceA); err == nil {
		t.Fatal("the device named at sign-out is still registered")
	}

	// No body, an unknown id, and a body that does not parse all sign out.
	for name, body := range map[string][]byte{
		"no body":    nil,
		"unknown id": []byte(`{"device_id":"` + deviceA + `"}`),
		"not an id":  []byte(`{"device_id":"nope"}`),
		"garbage":    []byte(`{"device_id":`),
	} {
		tok := login(t, h)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: logout %d", name, rec.Code)
		}
		if _, err := st.GetSession(ctx, tok); err == nil {
			t.Fatalf("%s: the session survived", name)
		}
	}
	if _, err := st.GetDevice(ctx, "bob", deviceB); err != nil {
		t.Fatal("bob's device went with sign-outs that did not name it")
	}
}

func TestDisablingOrDeletingAUserRevokesTheirDevices(t *testing.T) {
	h, st := newTestServerStore(t)
	ctx := context.Background()
	adminTok := login(t, h)
	bobTok := seedUser(t, st, "bob", rbac.RoleReadOnly)
	carolTok := seedUser(t, st, "carol", rbac.RoleReadOnly)
	do(t, h, http.MethodPost, "/api/v1/devices", adminTok, registration(t, deviceA, tokenA))
	do(t, h, http.MethodPost, "/api/v1/devices", bobTok, registration(t, deviceB, tokenA2))
	const deviceC = "3b241101-e2bb-4255-8caf-4136c566a962"
	do(t, h, http.MethodPost, "/api/v1/devices", carolTok, registration(t, deviceC, tokenA2))

	if rec := do(t, h, http.MethodPut, "/api/v1/users/bob", adminTok, map[string]any{"disabled": true}); rec.Code != http.StatusOK {
		t.Fatalf("disable bob: %d %s", rec.Code, rec.Body.String())
	}
	if ds, _ := st.ListDevicesByUser(ctx, "bob"); len(ds) != 0 {
		t.Fatalf("a disabled user kept %d devices", len(ds))
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/users/carol", adminTok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete carol: %d", rec.Code)
	}
	if _, err := st.GetDevice(ctx, "carol", deviceC); err == nil {
		t.Fatal("a deleted user's device is still registered")
	}
	if _, err := st.GetDevice(ctx, adminID(t, st), deviceA); err != nil {
		t.Fatal("the admin's device went with somebody else's account")
	}
}

// Registration is audited like every other write, against the device, and the
// audit log never holds the token.
func TestDeviceRegistrationIsAuditedWithoutTheToken(t *testing.T) {
	h, st := newTestServerStore(t)
	tok := login(t, h)
	do(t, h, http.MethodPost, "/api/v1/devices", tok, registration(t, deviceA, tokenA))
	do(t, h, http.MethodPatch, "/api/v1/devices/"+deviceA+"/rules", tok, map[string]any{"alive": false})

	entries, err := st.ListAudit(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	var reg, patch bool
	for _, e := range entries {
		raw, _ := json.Marshal(e)
		if strings.Contains(strings.ToLower(string(raw)), tokenA) {
			t.Fatalf("an audit entry holds the token: %s", raw)
		}
		switch e.Action {
		case "POST /devices":
			reg = e.TargetType == "device"
		case "PATCH /devices/{id}/rules":
			patch = e.TargetType == "device" && e.TargetID == deviceA
		}
	}
	if !reg || !patch {
		t.Fatalf("registration audited: %v, rules change audited against the device: %v", reg, patch)
	}
}
