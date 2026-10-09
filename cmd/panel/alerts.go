package main

import (
	"context"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/briggleman/kraken/internal/panel/alerts"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/push"
	"github.com/briggleman/kraken/internal/panel/store"
)

// buildAlerts returns the push-alert dispatcher (#348). With no relay
// configured it is one with no sender, which accepts every event and sends
// nothing — push is off, and the Panel loses alerts and nothing else. A relay
// client that cannot be built (no install id) is the same: the Panel still
// starts, and says why its alerts are off.
//
// It logs exactly one line saying which: the relay's host, never its path or
// query and never the token.
func buildAlerts(ctx context.Context, cfg *config.Config, st store.Store, logger *slog.Logger) *alerts.Dispatcher {
	if !cfg.PushEnabled() {
		logger.Info("push alerts off (KRAKEN_PUSH_RELAY_URL is not set)")
		return alerts.NewDispatcher(st, nil, logger)
	}
	host := relayHost(cfg.PushRelayURL)
	if insecureRelayToken(cfg.PushRelayURL, cfg.PushRelayToken) {
		logger.Warn("KRAKEN_PUSH_RELAY_TOKEN is sent in the clear: KRAKEN_PUSH_RELAY_URL is plain http:// to a host "+
			"that is not loopback or a private address, so anyone on the path can read the token — use https://",
			"relay_host", host)
	}
	panelID, err := st.PanelID(ctx)
	if err != nil {
		logger.Error("push alerts off: could not read the panel's install id", "relay_host", host, "err", err)
		return alerts.NewDispatcher(st, nil, logger)
	}
	client, err := push.NewClient(cfg.PushRelayURL, panelID, cfg.PushRelayToken, push.WithLogger(logger))
	if err != nil {
		logger.Error("push alerts off: the relay client could not be built", "relay_host", host, "err", err)
		return alerts.NewDispatcher(st, nil, logger)
	}
	logger.Info("push alerts on", "relay_host", host, "relay_token", cfg.PushRelayToken != "")
	return alerts.NewDispatcher(st, client, logger, alerts.WithContext(ctx))
}

// relayHost is the host (and port) of the relay URL, for log lines.
func relayHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Host
}

// insecureRelayToken reports whether a relay token would cross the network in
// the clear to somewhere that is not this machine or the LAN: a token is set
// and the URL is plain http:// to a host that is neither loopback nor a
// private address. A host given as a name is not known to be either — what it
// resolves to can change — so only "localhost" itself is taken as local.
func insecureRelayToken(rawURL, token string) bool {
	if token == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
