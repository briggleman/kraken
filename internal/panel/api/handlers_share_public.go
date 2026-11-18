package api

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/share"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// The public side of a server share link (#475): GET /s/{token}. No session
// — the link is the whole authority, and it is read-only. A lookup never
// consumes the link: a chat service fetches it several times, and every
// friend who taps it fetches it again.
//
// The page is built from share.Card, which holds only what the link may
// show. It is filled field by field below from an allowlist; the server row
// itself never reaches the template.

// shareTouchEvery throttles last_seen_at writes: a chat service's burst of
// fetches is one sighting, not a dozen writes.
const shareTouchEvery = time.Minute

// handleSharePage serves the card for a live link, and the same plain 404
// page for a link that is unknown, revoked or expired — the three are not
// told apart, so a guess learns nothing.
func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")

	token := chi.URLParam(r, "token")
	now := time.Now().UTC()
	link, err := s.store.GetShareLinkByHash(ctx, store.HashToken(token))
	if err != nil || !link.Live(now) {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.logger.Warn("share link lookup failed", "err", err)
		}
		s.writeSharePage(w, http.StatusNotFound, nil)
		return
	}
	sv, err := s.store.GetServer(ctx, link.ServerID)
	if err != nil {
		s.writeSharePage(w, http.StatusNotFound, nil)
		return
	}
	card := s.shareCard(ctx, link, sv, token, now)
	if link.LastSeenAt == nil || now.Sub(*link.LastSeenAt) >= shareTouchEvery {
		if err := s.store.TouchShareLink(ctx, link.ID, now); err != nil {
			s.logger.Warn("share link touch failed", "err", err)
		}
	}
	s.writeSharePage(w, http.StatusOK, card)
}

// writeSharePage renders into a buffer first, so a template failure is a
// clean 500 rather than half a page under a 200.
func (s *Server) writeSharePage(w http.ResponseWriter, status int, card *share.Card) {
	var buf bytes.Buffer
	var err error
	if card == nil {
		err = share.RenderNotFound(&buf)
	} else {
		err = share.Render(&buf, card)
	}
	if err != nil {
		s.logger.Error("share page render failed", "err", err)
		http.Error(w, "could not render this page", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// shareCard builds the card from an allowlist: the server's display name, its
// spec's name and banner, a collapsed state, the last-known player count,
// and the join values the link's flags and the spec's join block allow.
func (s *Server) shareCard(ctx context.Context, link *store.ShareLink, sv *store.Server, token string, now time.Time) *share.Card {
	c := &share.Card{ServerName: sv.Name, AsOf: now}
	public := s.publicURL(ctx)
	if public != "" {
		c.PageURL = public + "/s/" + token
		c.SiteName = "Kraken · " + hostOf(public)
	} else {
		c.SiteName = "Kraken"
	}

	sp, _ := s.store.GetSpec(ctx, sv.SpecID)
	if sp != nil {
		c.Game = sp.Name
		if isHTTPSURL(sp.BannerURL) {
			c.BannerURL = sp.BannerURL
			c.ImageURL = sp.BannerURL
		}
	} else {
		c.Game = "Game server"
	}

	node, _ := s.store.GetNode(ctx, hostNodeID(sv))
	c.State = shareState(sv.State, node)
	if c.State == share.StateRunning && sv.PlayersKnown {
		c.PlayersKnown = true
		c.Players = int(sv.Players)
		c.MaxPlayers = int(sv.MaxPlayers)
	}
	if sp != nil && sp.Join != nil {
		c.Join = shareJoinValues(link, sv, sp, node)
		if len(c.Join) > 0 {
			c.Notes = sp.Join.Notes
		}
	}
	return c
}

// shareState collapses a server's lifecycle into the four states a card shows.
// A node that is not online makes every server on it unreachable: the Panel
// cannot vouch for a state it cannot read.
func shareState(st store.ServerState, node *cluster.Node) string {
	if node == nil || node.Status != cluster.NodeOnline {
		return share.StateUnreachable
	}
	switch st {
	case store.StateRunning:
		return share.StateRunning
	case store.StateStarting, store.StateInstalling, store.StateRestoring:
		return share.StateStarting
	default:
		return share.StateStopped
	}
}

// shareJoinValues returns the values the link may show, in the order a player
// needs them: the address or invite code, then the password. Each is gated on
// the link's own flag; a value that is blank or cannot be built is left out
// rather than shown empty.
func shareJoinValues(link *store.ShareLink, sv *store.Server, sp *spec.Spec, node *cluster.Node) []share.JoinValue {
	j := sp.Join
	var out []share.JoinValue
	if link.ShowAddress {
		switch j.Method {
		case spec.JoinAddress:
			if addr := shareAddress(sv, node, j.Port); addr != "" {
				out = append(out, share.JoinValue{Kind: share.JoinAddress, Value: addr})
			}
		case spec.JoinInviteCode:
			if code := joinKeyValue(sv, sp, j.Code); code != "" {
				out = append(out, share.JoinValue{Kind: share.JoinCode, Value: code})
			}
		}
	}
	if link.ShowPassword && j.Password != "" {
		if pw := joinKeyValue(sv, sp, j.Password); pw != "" {
			out = append(out, share.JoinValue{Kind: share.JoinPassword, Value: pw})
		}
	}
	return out
}

// joinKeyValue reads a key the spec's join block names, the way the rendered
// config sees it: a settings field's effective value, else the server's
// variable, else the variable's spec default. Validate guarantees the key is
// one of the spec's own, so nothing else from the server can be named here.
func joinKeyValue(sv *store.Server, sp *spec.Spec, key string) string {
	if v, ok := sp.ResolveSettings(sv.Settings)[key]; ok {
		return strings.TrimSpace(v)
	}
	for _, v := range sp.Variables {
		if v.Key != key {
			continue
		}
		if val, ok := sv.Vars[key]; ok {
			return strings.TrimSpace(val)
		}
		return strings.TrimSpace(v.Default)
	}
	return ""
}

// shareAddress is host:port for an address join: the server's DNS name, else
// the node's detected WAN IP, else its operator-set public host — and never a
// private, loopback or link-local address, which would be the operator's LAN
// handed to a stranger. The port is always the allocated host port of the
// spec port players connect to; no port, no address.
func shareAddress(sv *store.Server, node *cluster.Node, portName string) string {
	port := sv.Ports[portName]
	if port <= 0 || port > 65535 {
		return ""
	}
	host := ""
	switch {
	case sv.DNS != nil && sv.DNS.Name != "":
		host = sv.DNS.Name
	case node != nil && publicHostOK(node.ExternalIP):
		host = node.ExternalIP
	case node != nil && publicHostOK(node.PublicHost):
		host = node.PublicHost
	}
	if host == "" {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// publicHostOK accepts a DNS name or a public IP, and refuses an empty host
// or any address a stranger could not reach or should not see.
func publicHostOK(h string) bool {
	h = strings.TrimSpace(h)
	if h == "" {
		return false
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		// A name. A single label ("behemoth") is a LAN machine name, not
		// something a player's resolver will find.
		return strings.Contains(h, ".") && !strings.HasSuffix(strings.ToLower(h), ".local")
	}
	return !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsLinkLocalMulticast())
}

// isHTTPSURL reports whether u is an absolute https URL, the only kind of
// artwork a public page will point a reader's browser at.
func isHTTPSURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && p.Scheme == "https" && p.Host != ""
}
