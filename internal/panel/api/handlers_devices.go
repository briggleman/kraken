package api

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

// Devices are the phones that receive push alerts (#348). The contract they are
// built against is docs/design/push-alerts.md. A device belongs to the user who
// registered it and is reached only through that user's session — there is no
// device credential — so every route here is session-authenticated, and an
// alert is filtered by the owner's permissions at the moment it is sent.

// The bounds a registration is held to.
const (
	// APNs tokens are 32 bytes today. Apple says they are variable length and
	// may grow, so anything up to 100 bytes is taken, and nothing shorter than
	// today's — a short one is a truncated paste, not a token.
	minAPNsTokenBytes = 32
	maxAPNsTokenBytes = 100
	// maxDeviceName bounds the display name, in characters. It is shown in a
	// list, and a phone's own name is far shorter.
	maxDeviceName = 100
	// maxAliveMutes bounds the mute list before any of it is looked up. Every
	// entry has to be a server the caller can view, so a real list is never
	// longer than the fleet.
	maxAliveMutes = 1000
)

type registerDeviceRequest struct {
	ID              string `json:"id"`
	Platform        string `json:"platform"`
	APNsToken       string `json:"apns_token"`
	APNsEnvironment string `json:"apns_environment"`
	PublicKey       string `json:"public_key"`
	Name            string `json:"name"`
}

// deviceView is a device as the API shows it. The APNs token and the public key
// stay out: the phone that registered them already has both, and nobody else
// needs them — the token in particular is half of what it takes to put an
// alert on somebody's phone.
type deviceView struct {
	ID              string            `json:"id"`
	UserID          string            `json:"user_id"`
	Platform        string            `json:"platform"`
	APNsEnvironment string            `json:"apns_environment"`
	Name            string            `json:"name"`
	Rules           store.DeviceRules `json:"rules"`
	CreatedAt       time.Time         `json:"created_at"`
	LastSeenAt      time.Time         `json:"last_seen_at"`
	LastSentAt      *time.Time        `json:"last_sent_at"`
	TokenInvalidAt  *time.Time        `json:"token_invalid_at"`
}

func toDeviceView(d *store.Device) deviceView {
	rules := d.Rules
	if rules.AliveMutedServers == nil {
		rules.AliveMutedServers = []string{}
	}
	return deviceView{
		ID: d.ID, UserID: d.UserID, Platform: d.Platform, APNsEnvironment: d.APNsEnvironment,
		Name: d.Name, Rules: rules, CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt,
		LastSentAt: d.LastSentAt, TokenInvalidAt: d.TokenInvalidAt,
	}
}

// handleRegisterDevice registers a phone, or refreshes its registration. The
// app calls it on every launch and whenever APNs hands it a new token, so it is
// idempotent on the install id and the answer is the same 200 either way: the
// device as it now stands, with its rules.
//
// An id another user registered is taken over, not refused. The id is the
// app's install id, so the only way a second user can present it is from that
// install, after the first user signed out of it — and that sign-out may not
// have reached the Panel (no network, the app deleted and reinstalled). The
// takeover starts the device afresh: the new owner gets default rules, never
// the old owner's mutes.
func (s *Server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	var req registerDeviceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	d, msg := validateDeviceRegistration(req)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	ctx := r.Context()
	user := userFrom(ctx)
	now := time.Now().UTC()
	d.UserID = user.ID
	d.Rules = store.DefaultDeviceRules()
	d.CreatedAt, d.LastSeenAt = now, now

	prev, err := s.store.GetDevice(ctx, d.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "could not load device")
		return
	}
	got, err := s.store.UpsertDevice(ctx, d)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not register device")
		return
	}
	switch {
	case prev == nil:
		s.logger.Info("push device registered", "device", got.ID, "user", user.Username)
	case prev.UserID != user.ID:
		s.logger.Info("push device moved to another user", "device", got.ID, "from_user_id", prev.UserID, "user", user.Username)
	}
	writeJSON(w, http.StatusOK, toDeviceView(got))
}

// validateDeviceRegistration checks a registration against the contract and
// returns the device it describes (without its owner, rules or times), or the
// reason it was refused. Nothing in a refusal echoes the token or the key.
func validateDeviceRegistration(req registerDeviceRequest) (*store.Device, string) {
	id, ok := parseDeviceID(req.ID)
	if !ok {
		return nil, "id must be a UUID (the app's install id)"
	}
	if req.Platform != store.DevicePlatformIOS {
		return nil, `platform must be "ios"`
	}
	if req.APNsEnvironment != store.APNsProduction && req.APNsEnvironment != store.APNsSandbox {
		return nil, `apns_environment must be "production" or "sandbox"`
	}
	token := strings.ToLower(strings.TrimSpace(req.APNsToken))
	raw, err := hex.DecodeString(token)
	if err != nil || len(raw) < minAPNsTokenBytes || len(raw) > maxAPNsTokenBytes {
		return nil, "apns_token must be the device token in hex (64 to 200 hex digits)"
	}
	key, ok := parseDevicePublicKey(req.PublicKey)
	if !ok {
		return nil, "public_key must be a base64 X25519 public key (32 bytes)"
	}
	name := strings.TrimSpace(req.Name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxDeviceName ||
		strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return nil, "name must be at most 100 printable characters"
	}
	return &store.Device{
		ID: id, Platform: req.Platform, APNsToken: token, APNsEnvironment: req.APNsEnvironment,
		PublicKey: key, Name: name,
	}, ""
}

// parseDeviceID accepts a UUID in any form uuid.Parse does and returns it
// canonical: iOS spells a UUID in upper case, and the same install must be the
// same row however it is spelled. The nil UUID is refused — an app that failed
// to mint its id would send it, and every such install would share one row.
func parseDeviceID(s string) (string, bool) {
	u, err := uuid.Parse(strings.TrimSpace(s))
	if err != nil || u == uuid.Nil {
		return "", false
	}
	return u.String(), true
}

// parseDevicePublicKey decodes the device's X25519 key and proves it usable.
// crypto/ecdh checks only the length of an X25519 key, so it is also put
// through one key agreement with a throwaway key: a low-order point (all
// zeros, or one of the handful of others) yields the all-zero secret, which
// ecdh refuses. Sealing to such a key would produce alerts anybody could open.
func parseDevicePublicKey(s string) ([]byte, bool) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return nil, false
	}
	probe, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, false
	}
	if _, err := probe.ECDH(pub); err != nil {
		return nil, false
	}
	return raw, true
}

// handleListDevices lists the caller's devices. `?user=` lists another user's,
// which takes user.manage — the same permission that disables or deletes that
// user, and with it their devices.
func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := userFrom(ctx).ID
	if q := r.URL.Query().Get("user"); q != "" && q != userID {
		if !roleFrom(ctx).Has(rbac.PermUserManage) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "missing permission: " + string(rbac.PermUserManage), "code": "forbidden",
			})
			return
		}
		if _, err := s.store.GetUser(ctx, q); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "user not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "could not load user")
			return
		}
		userID = q
	}
	devices, err := s.store.ListDevicesByUser(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list devices")
		return
	}
	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		views = append(views, toDeviceView(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": views})
}

type deviceRulesRequest struct {
	Attend            *bool     `json:"attend"`
	Healed            *bool     `json:"healed"`
	Alive             *bool     `json:"alive"`
	AliveMutedServers *[]string `json:"alive_muted_servers"`
}

// handleUpdateDeviceRules changes what a device hears about. Only its owner may:
// the rules are a person's choice about their own phone, and an administrator
// who wants a device silent revokes it instead. Fields left out keep their
// value; alive_muted_servers, when present, replaces the list.
func (s *Server) handleUpdateDeviceRules(w http.ResponseWriter, r *http.Request) {
	var req deviceRulesRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := r.Context()
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	if d.UserID != userFrom(ctx).ID {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "only the device's owner can change its rules", "code": "forbidden",
		})
		return
	}
	rules := d.Rules
	if req.Attend != nil {
		rules.Attend = *req.Attend
	}
	if req.Healed != nil {
		rules.Healed = *req.Healed
	}
	if req.Alive != nil {
		rules.Alive = *req.Alive
	}
	if req.AliveMutedServers != nil {
		mutes, msg := s.validateAliveMutes(r, *req.AliveMutedServers)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		rules.AliveMutedServers = mutes
	}
	if err := s.store.UpdateDeviceRules(ctx, d.ID, rules); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not update device rules")
		return
	}
	d.Rules = rules
	writeJSON(w, http.StatusOK, toDeviceView(d))
}

// validateAliveMutes accepts only servers the caller can view — the same check
// GET /servers/{id} makes — so a mute list cannot be used to learn which server
// ids exist, and the refusal reads the same for a server that is not there and
// one that is not the caller's. Duplicates are dropped, first one kept.
func (s *Server) validateAliveMutes(r *http.Request, ids []string) ([]string, string) {
	if len(ids) > maxAliveMutes {
		return nil, "alive_muted_servers is longer than the fleet could be"
	}
	out := make([]string, 0, len(ids))
	if len(ids) == 0 {
		return out, ""
	}
	ctx := r.Context()
	if !roleFrom(ctx).Has(rbac.PermServerView) {
		return nil, "alive_muted_servers: muting a server takes server.view"
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, "could not check alive_muted_servers"
	}
	visible := make(map[string]bool, len(servers))
	for _, sv := range servers {
		if s.mayAccessServer(ctx, sv) {
			visible[sv.ID] = true
		}
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !visible[id] {
			return nil, "alive_muted_servers: no server " + quoteForMessage(id) + " that you can view"
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, ""
}

// quoteForMessage quotes a caller-supplied value for an error message, cut
// short so a refusal cannot be made to carry an arbitrary payload back.
func quoteForMessage(s string) string {
	const limit = 64
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit]) + "…"
	}
	b, _ := json.Marshal(s)
	return string(b)
}

// handleDeleteDevice revokes a device: its owner can, and so can a user with
// user.manage, who can already disable the owner and revoke every device they
// have. The device stops receiving alerts at once; the app learns it on its
// next registration, which simply registers it again if the user is still
// signed in.
func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	if d.UserID != userFrom(ctx).ID && !roleFrom(ctx).Has(rbac.PermUserManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "missing permission: " + string(rbac.PermUserManage), "code": "forbidden",
		})
		return
	}
	if err := s.store.DeleteDevice(ctx, d.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "could not revoke device")
		return
	}
	s.logger.Info("push device revoked", "device", d.ID, "by", userFrom(ctx).Username)
	writeJSON(w, http.StatusNoContent, nil)
}

// loadDevice resolves the {id} path parameter, writing the 404 itself when there
// is no such device. A malformed id is a 404 too: it names no device.
func (s *Server) loadDevice(w http.ResponseWriter, r *http.Request) (*store.Device, bool) {
	id, ok := parseDeviceID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "device not found")
		return nil, false
	}
	d, err := s.store.GetDevice(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "device not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load device")
		return nil, false
	}
	return d, true
}

// revokeOwnDevice deletes the device with this id when it belongs to userID —
// the sign-out path, where the app names the install it is signing out of. An
// id that is malformed, unknown or somebody else's is ignored: sign-out must
// succeed whatever the app sends, and must never revoke another user's phone.
func (s *Server) revokeOwnDevice(r *http.Request, userID, rawID string) {
	id, ok := parseDeviceID(rawID)
	if !ok {
		return
	}
	ctx := r.Context()
	d, err := s.store.GetDevice(ctx, id)
	if err != nil || d.UserID != userID {
		return
	}
	if err := s.store.DeleteDevice(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.logger.Warn("sign-out: could not revoke push device", "device", id, "err", err)
		return
	}
	s.logger.Info("push device revoked at sign-out", "device", id)
}
