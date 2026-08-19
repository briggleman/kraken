// Package tunnel implements the reverse-connection transport: an Agent dials
// out to the Panel over mutual TLS and keeps one multiplexed session open, and
// the Panel routes its ordinary NodeService gRPC connections back through it.
// See docs/design/reverse-connections.md.
//
// Identity is cryptographic end to end: the Agent's client certificate carries
// a Panel-minted identity as a URI SAN (kraken://agent/<id>), so no application
// handshake exists to spoof — the TLS layer *is* the handshake. The Panel maps
// that identity to a node record and rejects anything unbound.
//
// Direction of multiplexing: the Agent is the yamux *server* (it accepts
// streams and feeds them to its gRPC server), the Panel is the yamux *client*
// (each gRPC connection it opens becomes one stream). The gRPC channel inside
// the tunnel is plaintext on purpose — the tunnel itself is the mTLS boundary,
// and TLS-in-TLS would buy nothing but latency.
package tunnel

import (
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

// DefaultPort is the Panel's reverse-tunnel listen port. Chosen next to the
// gRPC/SFTP defaults (9090/2022) but Panel-side: agents dial it, nothing on a
// node ever listens on it.
const DefaultPort = 9443

// Scheme prefixes a nodeclient dial target that must be routed through a live
// tunnel session instead of TCP: "tunnel:<node-id>".
const Scheme = "tunnel:"

// Target renders the nodeclient dial target for a tunnel-mode node.
func Target(nodeID string) string { return Scheme + nodeID }

// keepalive tuning: a dropped tunnel should read as offline in seconds, not
// TCP-timeout minutes. yamux pings both directions; 3 missed intervals kills
// the session on either end.
const (
	keepaliveInterval = 10 * time.Second
	connWriteTimeout  = 15 * time.Second
)

// Config returns the yamux session config both sides share. yamux's internal
// log output is discarded — it has no levels; both sides surface session
// lifecycle through their own slog loggers instead.
func Config() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = keepaliveInterval
	cfg.ConnectionWriteTimeout = connWriteTimeout
	cfg.LogOutput = io.Discard
	return cfg
}
