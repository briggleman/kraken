package api

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/push"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

// Devices are the phones that receive push alerts (#348). The contract they are
// built against is docs/design/push-alerts.md. A device belongs to the user who
// registered it and is reached only through that user's session — there is no
// device credential — so every route here is session-authenticated, and an
// alert is filtered by the owner's permissions at the moment it is sent.
//
// A device is the pair (user, install id), never the id alone. Device ids are
// not secret — they are audit targets — so nothing here acts on an id without
// also naming whose: a route acts on the caller's own row with that id, and an
// administrator reaches another user's only by naming the user.

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

// decodeClientJSON is decodeJSON without the unknown-field refusal, for the two
// bodies the companion app sends: registration and rules. Everywhere else the
// Panel and its client ship together, so a field the Panel does not know is a
// mistake worth a 400. These come from an app released through the App Store
// on its own schedule, talking to Panels that are self-hosted and upgraded
// whenever their operators get to it; the first app release to add a field
// would be refused by every older Panel, and the phone would silently stop
// hearing anything. The size cap is the same.
func decodeClientJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONBody)).Decode(v)
}

// handleRegisterDevice registers the caller's phone, or refreshes their
// registration of it. The app calls it on every launch and whenever APNs hands
// it a new token, so it is idempotent on the install id and the answer is the
// same 200 either way: the device as it now stands, with its rules.
//
// Another user's registration of the same install id is a separate row and is
// never touched: an install signed into two accounts hears both until it signs
// out of one, and sign-out names the install so that it can.
func (s *Server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	var req registerDeviceRequest
	if err := decodeClientJSON(r, &req); err != nil {
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

	_, err := s.store.GetDevice(ctx, user.ID, d.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "could not load device")
		return
	}
	isNew := err != nil
	got, err := s.store.UpsertDevice(ctx, d)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not register device")
		return
	}
	if isNew {
		s.logger.Info("push device registered", "device", got.ID, "user", user.Username)
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
// to mint its id would send it, and every such install of one user would
// collapse into one row.
func parseDeviceID(s string) (string, bool) {
	u, err := uuid.Parse(strings.TrimSpace(s))
	if err != nil || u == uuid.Nil {
		return "", false
	}
	return u.String(), true
}

// parseDevicePublicKey decodes the device's X25519 key and holds it to the
// same test the seal applies (push.ValidatePublicKey), so a key accepted here
// is one every alert can be sealed to — one definition, not two that drift.
func parseDevicePublicKey(s string) ([]byte, bool) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || push.ValidatePublicKey(raw) != nil {
		return nil, false
	}
	return raw, true
}

// handleListDevices lists the caller's devices. `?user=` lists another user's,
// which takes user.manage — the same permission that disables or deletes that
// user, and with it their devices.
func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.deviceOwner(w, r)
	if !ok {
		return
	}
	devices, err := s.store.ListDevicesByUser(r.Context(), userID)
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

// deviceOwner resolves whose devices a request means: the caller's, or — with
// `?user=` naming somebody else — that user's, which takes user.manage and a
// user who exists. It writes the refusal itself.
func (s *Server) deviceOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	ctx := r.Context()
	self := userFrom(ctx).ID
	q := r.URL.Query().Get("user")
	if q == "" || q == self {
		return self, true
	}
	if !roleFrom(ctx).Has(rbac.PermUserManage) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "missing permission: " + string(rbac.PermUserManage), "code": "forbidden",
		})
		return "", false
	}
	if _, err := s.store.GetUser(ctx, q); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return "", false
		}
		writeError(w, http.StatusInternalServerError, "could not load user")
		return "", false
	}
	return q, true
}

type deviceRulesRequest struct {
	Attend            *bool     `json:"attend"`
	Healed            *bool     `json:"healed"`
	Alive             *bool     `json:"alive"`
	AliveMutedServers *[]string `json:"alive_muted_servers"`
}

// handleUpdateDeviceRules changes what one of the caller's own devices hears
// about. There is no `?user=` here: the rules are a person's choice about their
// own phone, and an administrator who wants a device silent revokes it
// instead. Fields left out keep their value; alive_muted_servers, when
// present, replaces the list.
func (s *Server) handleUpdateDeviceRules(w http.ResponseWriter, r *http.Request) {
	var req deviceRulesRequest
	if err := decodeClientJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := r.Context()
	userID := userFrom(ctx).ID
	d, ok := s.loadDevice(w, r, userID)
	if !ok {
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
	if err := s.store.UpdateDeviceRules(ctx, userID, d.ID, rules); err != nil {
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

// handleDeleteDevice revokes one of the caller's devices or, with `?user=`
// and user.manage, one of another user's — the permission that can already
// disable that user and revoke every device they have. The device stops
// receiving alerts at once; the app learns it on its next registration, which
// simply registers it again if the user is still signed in.
func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.deviceOwner(w, r)
	if !ok {
		return
	}
	d, ok := s.loadDevice(w, r, userID)
	if !ok {
		return
	}
	if err := s.store.DeleteDevice(r.Context(), userID, d.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "could not revoke device")
		return
	}
	s.logger.Info("push device revoked", "device", d.ID, "user_id", userID, "by", userFrom(r.Context()).Username)
	writeJSON(w, http.StatusNoContent, nil)
}

// loadDevice resolves the {id} path parameter to the user's device with that
// id, writing the 404 itself when there is none. A malformed id is a 404 too:
// it names no device.
func (s *Server) loadDevice(w http.ResponseWriter, r *http.Request, userID string) (*store.Device, bool) {
	id, ok := parseDeviceID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "device not found")
		return nil, false
	}
	d, err := s.store.GetDevice(r.Context(), userID, id)
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

// handleTestDevice sends a test alert to one of the caller's own devices and
// answers with how it went: sent, token_dead, rejected or dropped, or disabled
// when this Panel has no relay. It waits for the outcome — that is the point of
// a test — but no longer than the dispatcher's test window, so a relay in
// trouble answers dropped well before a proxy gives up on the request. The
// device gets it whatever its rules say, and a dead token is tried again: it
// is how the person finds out.
func (s *Server) handleTestDevice(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	d, ok := s.loadDevice(w, r, user.ID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.alerts.SendTest(r.Context(), d, user.Username, panelHost(r)))
}

// panelHost is the Panel's name as the caller reached it, for the test alert's
// sentence: the host the phone used is the one its owner will recognise.
func panelHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return "the panel"
	}
	return host
}

// revokeOwnDevice deletes the user's device with this id — the sign-out path,
// where the app names the install it is signing out of. An id that is
// malformed or unknown is ignored, and another user's registration of the same
// install is out of reach by the key: sign-out must succeed whatever the app
// sends, and must never revoke another user's phone.
func (s *Server) revokeOwnDevice(r *http.Request, userID, rawID string) {
	id, ok := parseDeviceID(rawID)
	if !ok {
		return
	}
	err := s.store.DeleteDevice(r.Context(), userID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		s.logger.Warn("sign-out: could not revoke push device", "device", id, "err", err)
	default:
		s.logger.Info("push device revoked at sign-out", "device", id)
	}
}
