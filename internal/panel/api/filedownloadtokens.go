package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"
)

// downloadTokenTTL is how long a minted file-download token stays redeemable.
// Deliberately short: the token rides in a URL (a plain <a download href>
// cannot set an Authorization header), so its whole safety argument is that it
// is single-use, scoped to one exact path set, and stops working within a
// minute of being minted.
const downloadTokenTTL = 60 * time.Second

// maxTokenPaths caps how many paths one token may cover. The zip route takes a
// list, and an unbounded list is unbounded in-memory state on the Panel.
const maxTokenPaths = 256

// Download-token kinds. A token minted for a single file's raw bytes is not
// redeemable on the zip route, and vice versa — the route a token was minted
// for is part of what it is bound to.
const (
	downloadKindRaw = "raw"
	downloadKindZip = "zip"
)

var (
	errDownloadTokenUnknown = errors.New("unknown or already-used download token")
	errDownloadTokenExpired = errors.New("download token expired")
)

// downloadGrant is what a redeemed token authorizes: one server, one exact set
// of paths, on one route, on behalf of the user who minted it. Everything a
// redemption is checked against lives here — nothing is taken from the URL
// except the token itself.
type downloadGrant struct {
	serverID string
	userID   string
	kind     string
	paths    []string // canonical, in mint order; the exact set that may stream
	hash     [32]byte // sha256 of the token, for the constant-time compare
	expires  time.Time
}

// downloadTokenRegistry holds one-time file-download tokens in memory, keyed by
// the SHA-256 of the token so the Panel never stores the secret it handed out.
// In memory on purpose, and never in Postgres: a token is valid for 60 seconds
// and a Panel restart simply invalidates the handful outstanding (the browser
// mints a fresh one per click). Same shape and hygiene as the Agent bootstrap
// registry in handlers_enroll.go — crypto/rand generation, hash at rest, a
// mutex, an expiry sweep, and deletion on redemption.
type downloadTokenRegistry struct {
	mu     sync.Mutex
	grants map[string]downloadGrant // key: hex(sha256(token))
}

func newDownloadTokenRegistry() *downloadTokenRegistry {
	return &downloadTokenRegistry{grants: map[string]downloadGrant{}}
}

// issue mints a token for the given grant and returns the opaque secret plus
// its expiry. The secret is returned once and never stored.
func (d *downloadTokenRegistry) issue(serverID, userID, kind string, paths []string, ttl time.Duration) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(token))
	exp := time.Now().Add(ttl)
	d.mu.Lock()
	d.grants[hex.EncodeToString(sum[:])] = downloadGrant{
		serverID: serverID,
		userID:   userID,
		kind:     kind,
		paths:    slices.Clone(paths),
		hash:     sum,
		expires:  exp,
	}
	// Opportunistically sweep anything that has aged out, so an idle Panel does
	// not hold grants for tokens nobody can redeem any more.
	now := time.Now()
	for k, g := range d.grants {
		if now.After(g.expires) {
			delete(d.grants, k)
		}
	}
	d.mu.Unlock()
	return token, exp, nil
}

// redeem consumes a token: the grant is DELETED on lookup whether or not it is
// still valid, so a token is good for at most one request and a replayed URL
// finds nothing. The caller still has to check the grant against the request
// (server, route kind, paths) and re-check the minting user's permission.
func (d *downloadTokenRegistry) redeem(token string) (downloadGrant, error) {
	if token == "" {
		return downloadGrant{}, errDownloadTokenUnknown
	}
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	d.mu.Lock()
	g, ok := d.grants[key]
	delete(d.grants, key) // one-time use, before the caller streams anything
	d.mu.Unlock()
	if !ok {
		return downloadGrant{}, errDownloadTokenUnknown
	}
	// The map lookup already matched the digest; compare it again in constant
	// time so the hit is never decided by a short-circuiting byte compare.
	if subtle.ConstantTimeCompare(g.hash[:], sum[:]) != 1 {
		return downloadGrant{}, errDownloadTokenUnknown
	}
	if time.Now().After(g.expires) {
		return downloadGrant{}, errDownloadTokenExpired
	}
	return g, nil
}

// count reports how many grants are outstanding. Test-facing hygiene check —
// the sweep and single-use deletion are the reason this stays bounded.
func (d *downloadTokenRegistry) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.grants)
}
