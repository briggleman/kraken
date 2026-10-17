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

	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/mail/smtptest"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

const invitePublic = "https://kraken.example.com"

// inviteSetup is a Panel with a working relay and a public address, ready to
// send invites, plus the relay to read them back from.
func inviteSetup(t *testing.T) (http.Handler, *memory.Store, string, *smtptest.Server) {
	t.Helper()
	h, st := newTestServerStore(t)
	token := login(t, h)
	relay := startRelay(t, smtptest.Options{})
	configureRelay(t, h, token, relay)
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"public_url": invitePublic}); rec.Code != http.StatusOK {
		t.Fatalf("public url: %d %s", rec.Code, rec.Body.String())
	}
	return h, st, token, relay
}

type invitedUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Invite   *struct {
		State     string    `json:"state"`
		SentTo    string    `json:"sent_to"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"invite"`
}

func createInvited(t *testing.T, h http.Handler, token, username, email string) invitedUser {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/users", token, map[string]any{
		"username": username, "email": email, "role_id": "operator", "invite": true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with invite: %d %s", rec.Code, rec.Body.String())
	}
	var u invitedUser
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

var inviteLinkRE = regexp.MustCompile(`https://kraken\.example\.com/invite/([A-Za-z0-9_-]+)`)

// inviteTokenFrom reads the one-time token out of the newest message the relay
// took, the way the invitee would: from the link in the email.
func inviteTokenFrom(t *testing.T, relay *smtptest.Server) string {
	t.Helper()
	msgs := relay.Messages()
	if len(msgs) == 0 {
		t.Fatal("the relay saw no message")
	}
	// The body is quoted-printable: a long line (the link) is folded with
	// "=\r\n" soft breaks, which an email client undoes before it shows the
	// link. Undo them here the same way.
	data := strings.ReplaceAll(msgs[len(msgs)-1].Data, "=\r\n", "")
	m := inviteLinkRE.FindStringSubmatch(data)
	if m == nil {
		t.Fatalf("no invite link in the message:\n%s", msgs[len(msgs)-1].Data)
	}
	return m[1]
}

func listUsers(t *testing.T, h http.Handler, token string) map[string]invitedUser {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/users", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list users: %d", rec.Code)
	}
	var resp struct {
		Users []invitedUser `json:"users"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	out := map[string]invitedUser{}
	for _, u := range resp.Users {
		out[u.Username] = u
	}
	return out
}

// The whole life of an invite: created with the account, read by the invitee,
// accepted once, signed in, and then dead.
func TestInvite_Lifecycle(t *testing.T) {
	h, st, token, relay := inviteSetup(t)

	u := createInvited(t, h, token, "captain", "captain@example.org")
	if u.Invite == nil || u.Invite.State != "sent" || u.Invite.SentTo != "captain@example.org" {
		t.Fatalf("created user carries no sent invite: %+v", u.Invite)
	}
	if got := time.Until(u.Invite.ExpiresAt); got < 23*time.Hour || got > 25*time.Hour {
		t.Fatalf("expiry %s from now, want about 24h", got)
	}

	// The email: the link is built from the public address, and the body
	// names the inviter, the account and the role.
	msg := relay.Messages()[0]
	if msg.To[0] != "captain@example.org" {
		t.Fatalf("sent to %v", msg.To)
	}
	for _, want := range []string{"Subject: " + testAdmin + " invited you to kraken.example.com", "captain", "Operator", "24 hours", "/invite/"} {
		if !strings.Contains(msg.Data, want) {
			t.Errorf("message lacks %q:\n%s", want, msg.Data)
		}
	}
	if strings.Contains(msg.Data, "{{") {
		t.Fatalf("unrendered template:\n%s", msg.Data)
	}
	tok := inviteTokenFrom(t, relay)

	// The users list shows the chip.
	if users := listUsers(t, h, token); users["captain"].Invite == nil || users["captain"].Invite.State != "sent" {
		t.Fatalf("list lacks the invite chip: %+v", users["captain"].Invite)
	}

	// Nobody can log in as the account yet: the random password is unknown,
	// and the refusal is the ordinary one.
	if rec := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "captain", "password": "guess"}); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid credentials") {
		t.Fatalf("login as invitee: %d %s", rec.Code, rec.Body.String())
	}

	// The invitee opens the link.
	rec := do(t, h, http.MethodGet, "/api/v1/invites/"+tok, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get invite: %d %s", rec.Code, rec.Body.String())
	}
	var view struct {
		Username, RoleName, PanelHost string
		ExpiresAt                     time.Time
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &struct {
		Username  *string    `json:"username"`
		RoleName  *string    `json:"role_name"`
		PanelHost *string    `json:"panel_host"`
		ExpiresAt *time.Time `json:"expires_at"`
	}{&view.Username, &view.RoleName, &view.PanelHost, &view.ExpiresAt})
	if view.Username != "captain" || view.RoleName != "Operator" || view.PanelHost != "kraken.example.com" {
		t.Fatalf("invite view: %+v", view)
	}

	// Too short, then good.
	if rec := do(t, h, http.MethodPost, "/api/v1/invites/"+tok+"/accept", "", map[string]string{"password": "short"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("short password: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v1/invites/"+tok+"/accept", "", map[string]string{"password": "chosen-by-captain-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	var lr struct {
		Token string `json:"token"`
		User  struct {
			Username           string `json:"username"`
			MustChangePassword bool   `json:"must_change_password"`
		} `json:"user"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	if lr.Token == "" || lr.User.Username != "captain" || lr.User.MustChangePassword {
		t.Fatalf("accept answered %s", rec.Body.String())
	}
	// The session works, and so does the chosen password.
	if rec := do(t, h, http.MethodGet, "/api/v1/auth/me", lr.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("me with invite session: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "captain", "password": "chosen-by-captain-1"}); rec.Code != http.StatusOK {
		t.Fatalf("login with chosen password: %d %s", rec.Code, rec.Body.String())
	}

	// The link is spent.
	if rec := do(t, h, http.MethodGet, "/api/v1/invites/"+tok, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("spent link still readable: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/invites/"+tok+"/accept", "", map[string]string{"password": "another-password-9"}); rec.Code != http.StatusNotFound {
		t.Fatalf("spent link redeemed twice: %d", rec.Code)
	}
	if users := listUsers(t, h, token); users["captain"].Invite != nil {
		t.Fatalf("chip remains after acceptance: %+v", users["captain"].Invite)
	}

	// The token appears nowhere the Panel keeps: not in the audit log, and
	// only as a digest in the store.
	rec = do(t, h, http.MethodGet, "/api/v1/audit?limit=100", token, nil)
	if strings.Contains(rec.Body.String(), tok) {
		t.Fatal("the invite token is in the audit log")
	}
	for _, want := range []string{"invite.sent:captain", "invite.accepted"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("audit lacks %q", want)
		}
	}
	inv, err := st.LatestInviteByUser(context.Background(), u.ID)
	if err != nil || inv.TokenHash == tok || len(inv.TokenHash) != 64 {
		t.Fatalf("stored invite: %+v (%v)", inv, err)
	}
}

// Accepting the invite ends every session the account had before.
func TestInvite_AcceptEndsOlderSessions(t *testing.T) {
	h, st, token, relay := inviteSetup(t)
	u := createInvited(t, h, token, "captain", "captain@example.org")
	tok := inviteTokenFrom(t, relay)
	// A session from before — however it came to exist.
	stale := "stale-session-token"
	if err := st.CreateSession(context.Background(), &store.Session{Token: stale, UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/auth/me", stale, nil); rec.Code != http.StatusOK {
		t.Fatalf("stale session should work before acceptance: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/invites/"+tok+"/accept", "", map[string]string{"password": "chosen-by-captain-1"}); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/auth/me", stale, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale session survived acceptance: %d", rec.Code)
	}
}

// The link is built from the public address, never from the Host header the
// request arrived with.
func TestInvite_LinkIgnoresRequestHost(t *testing.T) {
	h, _, token, relay := inviteSetup(t)
	body, _ := json.Marshal(map[string]any{"username": "captain", "email": "captain@example.org", "role_id": "operator", "invite": true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(string(body)))
	req.Host = "evil.example.net"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	data := relay.Messages()[0].Data
	if strings.Contains(data, "evil.example.net") || !strings.Contains(data, invitePublic+"/invite/") {
		t.Fatalf("the link followed the Host header:\n%s", data)
	}
}

func TestInvite_RefusedWithoutRelayOrPublicURL(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	body := map[string]any{"username": "captain", "email": "captain@example.org", "role_id": "operator", "invite": true}

	rec := do(t, h, http.MethodPost, "/api/v1/users", token, body)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "outbound email is not configured") {
		t.Fatalf("no relay: %d %s", rec.Code, rec.Body.String())
	}
	relay := startRelay(t, smtptest.Options{})
	configureRelay(t, h, token, relay)
	rec = do(t, h, http.MethodPost, "/api/v1/users", token, body)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no public address") {
		t.Fatalf("no public url: %d %s", rec.Code, rec.Body.String())
	}
	// Nothing was created on either refusal.
	if users := listUsers(t, h, token); len(users) != 1 {
		t.Fatalf("a refused invite created an account: %v", users)
	}
	// An invite needs somewhere to go.
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"public_url": invitePublic}); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	noEmail := map[string]any{"username": "captain", "role_id": "operator", "invite": true}
	if rec := do(t, h, http.MethodPost, "/api/v1/users", token, noEmail); rec.Code != http.StatusBadRequest {
		t.Fatalf("invite without email: %d %s", rec.Code, rec.Body.String())
	}
	// The password path is untouched.
	if rec := do(t, h, http.MethodPost, "/api/v1/users", token, map[string]any{"username": "plain", "password": "plain-password-1", "role_id": "operator"}); rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), `"invite"`) {
		t.Fatalf("plain create: %d %s", rec.Code, rec.Body.String())
	}
}

// The relay refusing the message leaves the account, says so, and the admin
// can resend once the relay is fixed.
func TestInvite_SendFailureThenResend(t *testing.T) {
	defer api.SetInviteTimingForTest(24*time.Hour, 0)()
	h, _, token, _ := inviteSetup(t)
	bad := startRelay(t, smtptest.Options{RejectRecipient: "550 5.1.1 no such mailbox"})
	configureRelay(t, h, token, bad)

	rec := do(t, h, http.MethodPost, "/api/v1/users", token, map[string]any{
		"username": "captain", "email": "captain@example.org", "role_id": "operator", "invite": true,
	})
	if rec.Code != http.StatusFailedDependency || !strings.Contains(rec.Body.String(), "no such mailbox") || !strings.Contains(rec.Body.String(), `"state":"failed"`) {
		t.Fatalf("send failure: %d %s", rec.Code, rec.Body.String())
	}
	users := listUsers(t, h, token)
	if users["captain"].Invite == nil || users["captain"].Invite.State != "failed" {
		t.Fatalf("list after failure: %+v", users["captain"].Invite)
	}

	good := startRelay(t, smtptest.Options{})
	configureRelay(t, h, token, good)
	rec = do(t, h, http.MethodPost, "/api/v1/users/"+users["captain"].ID+"/invite", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"sent"`) {
		t.Fatalf("resend: %d %s", rec.Code, rec.Body.String())
	}
	tok := inviteTokenFrom(t, good)
	if rec := do(t, h, http.MethodGet, "/api/v1/invites/"+tok, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("resent link: %d", rec.Code)
	}
}

func TestInvite_ResendSupersedesAndCoolsDown(t *testing.T) {
	h, _, token, relay := inviteSetup(t)
	u := createInvited(t, h, token, "captain", "captain@example.org")
	first := inviteTokenFrom(t, relay)

	// Straight after the first send: too soon.
	rec := do(t, h, http.MethodPost, "/api/v1/users/"+u.ID+"/invite", token, nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("resend inside the cooldown: %d %s", rec.Code, rec.Body.String())
	}
	restore := api.SetInviteTimingForTest(24*time.Hour, 0)
	defer restore()
	rec = do(t, h, http.MethodPost, "/api/v1/users/"+u.ID+"/invite", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("resend: %d %s", rec.Code, rec.Body.String())
	}
	second := inviteTokenFrom(t, relay)
	if first == second {
		t.Fatal("resend reused the token")
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/invites/"+first, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("superseded link still live: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/invites/"+second, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("new link not live: %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/audit?limit=100", token, nil)
	if !strings.Contains(rec.Body.String(), "invite.resent:captain") {
		t.Fatal("resend not audited")
	}
}

func TestInvite_RevokeExpireDisable(t *testing.T) {
	h, _, token, relay := inviteSetup(t)

	// Revoked by the admin.
	u := createInvited(t, h, token, "captain", "captain@example.org")
	tok := inviteTokenFrom(t, relay)
	rec := do(t, h, http.MethodDelete, "/api/v1/users/"+u.ID+"/invite", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"revoked":1`) {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/invites/"+tok, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("revoked link still live: %d", rec.Code)
	}
	if users := listUsers(t, h, token); users["captain"].Invite != nil {
		t.Fatalf("chip after revoke: %+v", users["captain"].Invite)
	}

	// Expired: the chip says so, the link is dead.
	restore := api.SetInviteTimingForTest(-time.Second, 0)
	v := createInvited(t, h, token, "ensign", "ensign@example.org")
	restore()
	if v.Invite == nil || v.Invite.State != "expired" {
		t.Fatalf("expired invite state: %+v", v.Invite)
	}
	tok = inviteTokenFrom(t, relay)
	if rec := do(t, h, http.MethodGet, "/api/v1/invites/"+tok, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("expired link live: %d", rec.Code)
	}

	// Disabled: the live link stops working, and the row is closed.
	w := createInvited(t, h, token, "mate", "mate@example.org")
	tok = inviteTokenFrom(t, relay)
	if rec := do(t, h, http.MethodPut, "/api/v1/users/"+w.ID, token, map[string]any{"disabled": true}); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/invites/"+tok, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled account's link live: %d", rec.Code)
	}
	if users := listUsers(t, h, token); users["mate"].Invite != nil {
		t.Fatalf("chip after disable: %+v", users["mate"].Invite)
	}
	// And a disabled account cannot be re-invited.
	if rec := do(t, h, http.MethodPost, "/api/v1/users/"+w.ID+"/invite", token, nil); rec.Code != http.StatusConflict {
		t.Fatalf("invite to disabled account: %d", rec.Code)
	}
}

// Guessing links is rate limited per address, and the first refusal in a
// window leaves one audit row.
func TestInvite_LookupRateLimited(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	for i := 0; i < api.InviteRedeemBurstForTest; i++ {
		if rec := do(t, h, http.MethodGet, "/api/v1/invites/not-a-real-token-"+string(rune('a'+i)), "", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("lookup %d: %d", i, rec.Code)
		}
	}
	rec := do(t, h, http.MethodGet, "/api/v1/invites/one-too-many", "", nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over budget: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/invites/one-too-many/accept", "", map[string]string{"password": "whatever-password"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("accept shares the budget: %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/audit?limit=100", token, nil)
	if n := strings.Count(rec.Body.String(), "rate limited (too many invite lookups"); n != 1 {
		t.Fatalf("rate-limit audit rows = %d, want 1", n)
	}
}

// Only user.manage may send or revoke; the public routes need nothing.
func TestInvite_Permissions(t *testing.T) {
	h, _, token, _ := inviteSetup(t)
	u := createInvited(t, h, token, "captain", "captain@example.org")
	if rec := do(t, h, http.MethodPost, "/api/v1/users", token, map[string]any{"username": "viewer", "password": "viewer-pass-123", "role_id": "readonly"}); rec.Code != http.StatusCreated {
		t.Fatal(rec.Code)
	}
	rec := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "viewer", "password": "viewer-pass-123"})
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	if rec := do(t, h, http.MethodPost, "/api/v1/users/"+u.ID+"/invite", lr.Token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer could resend: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/users/"+u.ID+"/invite", lr.Token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer could revoke: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/users", lr.Token, map[string]any{"username": "x", "email": "x@example.org", "role_id": "operator", "invite": true}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer could invite: %d", rec.Code)
	}
}

// Deleting the user deletes their invite; the housekeeping pass removes
// closed rows past the window and leaves open ones alone.
func TestInvite_DeleteUserAndPrune(t *testing.T) {
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	relay := startRelay(t, smtptest.Options{})
	configureRelay(t, h, token, relay)
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"public_url": invitePublic}); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	u := createInvited(t, h, token, "captain", "captain@example.org")
	tok := inviteTokenFrom(t, relay)
	if rec := do(t, h, http.MethodDelete, "/api/v1/users/"+u.ID, token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete user: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/invites/"+tok, "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted user's link live: %d", rec.Code)
	}
	if _, err := st.LatestInviteByUser(context.Background(), u.ID); err == nil {
		t.Fatal("invite row survived the user")
	}

	// A fresh open invite is never pruned; a revoked one older than the
	// window is.
	v := createInvited(t, h, token, "ensign", "ensign@example.org")
	srv.PruneInvitesOnceForTest(context.Background())
	if _, err := st.LatestInviteByUser(context.Background(), v.ID); err != nil {
		t.Fatal("an open invite was pruned")
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/users/"+v.ID+"/invite", token, nil); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	if n, err := st.DeleteInvitesBefore(context.Background(), time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("prune of a closed row: n=%d err=%v", n, err)
	}
}
