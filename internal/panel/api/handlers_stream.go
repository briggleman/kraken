package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// streamSubprotocol is the WebSocket subprotocol used to carry the session token
// in the handshake instead of the query string, keeping it out of URLs/logs.
const streamSubprotocol = "kraken.token"

// streamToken extracts the session token for a stream WS handshake. Browsers
// can't set Authorization on a WS upgrade, so the client offers the token as a
// subprotocol (["kraken.token", "<token>"]); we fall back to the ?token= query
// param for compatibility.
func streamToken(r *http.Request) string {
	if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		parts := strings.Split(proto, ",")
		for i, p := range parts {
			if strings.TrimSpace(p) == streamSubprotocol && i+1 < len(parts) {
				return strings.TrimSpace(parts[i+1])
			}
		}
	}
	return r.URL.Query().Get("token")
}

// frame is a message pushed to the browser over the stream WebSocket.
type frame struct {
	Type string `json:"type"` // "console" | "stats" | "error"

	// console
	Ts     int64  `json:"ts,omitempty"`
	Stream string `json:"stream,omitempty"`
	Text   string `json:"text,omitempty"`

	// stats
	CPUPercent    float64 `json:"cpu_percent,omitempty"`
	MemUsedMB     int64   `json:"mem_used_mb,omitempty"`
	MemLimitMB    int64   `json:"mem_limit_mb,omitempty"`
	NetRxBytes    int64   `json:"net_rx_bytes,omitempty"`
	NetTxBytes    int64   `json:"net_tx_bytes,omitempty"`
	UptimeSeconds int64   `json:"uptime_seconds,omitempty"`
	DiskUsedMB    int64   `json:"disk_used_mb,omitempty"`
	Players       int32   `json:"players,omitempty"`
	MaxPlayers    int32   `json:"max_players,omitempty"`
	PlayersKnown  bool    `json:"players_known,omitempty"`

	// error
	Message string `json:"message,omitempty"`
}

// inbound is a message received from the browser.
type inbound struct {
	Type    string `json:"type"` // "command"
	Command string `json:"command"`
}

// handleServerStream upgrades to a WebSocket that multiplexes a server's live
// console and resource stats from the hosting Agent, and forwards console
// commands back. It authenticates from the `token` query param (browsers can't
// set Authorization on a WebSocket handshake).
func (s *Server) handleServerStream(w http.ResponseWriter, r *http.Request) {
	user, role, err := s.resolveSession(r.Context(), streamToken(r))
	if err != nil {
		ae := err.(*authError)
		writeError(w, ae.status, ae.Error())
		return
	}
	if !role.Has(rbac.PermServerConsoleRead) {
		writeError(w, http.StatusForbidden, "missing permission: "+string(rbac.PermServerConsoleRead))
		return
	}
	id := chi.URLParam(r, "id")
	server, err := s.store.GetServer(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	// Object-level authz: the owner, or a role with server.any (Owner/Admin).
	if !(role.Has(rbac.PermServerAny) || (server.OwnerID != "" && server.OwnerID == user.ID)) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	node, err := s.store.GetNode(r.Context(), server.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load node")
		return
	}
	client, err := s.nodes.Client(node.Address)
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not connect to agent")
		return
	}

	// Restrict cross-origin WS upgrades to the configured allowlist (same-origin
	// is always permitted by coder/websocket). Default to localhost dev origins so
	// the Vite proxy (browser :5173 → panel :8080) works without extra config;
	// production sets KRAKEN_ALLOWED_ORIGINS to its panel host.
	origins := s.allowedOrigins(r.Context())
	if len(origins) == 0 {
		origins = []string{"localhost:*", "127.0.0.1:*", "[::1]:*"}
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: origins,
		Subprotocols:   []string{streamSubprotocol},
	})
	if err != nil {
		return // Accept already wrote the response
	}
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frames := make(chan frame, 128)
	canCommand := role.Has(rbac.PermServerConsoleCommand)

	// Single writer goroutine — coder/websocket allows one concurrent writer.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-frames:
				if err := wsjson.Write(ctx, c, f); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	push := func(f frame) {
		select {
		case frames <- f:
		case <-ctx.Done():
		}
	}

	// Console stream → frames.
	go func() {
		stream, err := client.StreamConsole(ctx, &agentpb.StreamConsoleRequest{ServerId: id, TailLines: 200})
		if err != nil {
			push(frame{Type: "error", Message: "console: " + err.Error()})
			return
		}
		for {
			line, err := stream.Recv()
			if err != nil {
				return
			}
			push(frame{Type: "console", Ts: line.TsUnixMs, Stream: line.Stream, Text: line.Text})
		}
	}()

	// Stats stream → frames.
	go func() {
		stream, err := client.StreamStats(ctx, &agentpb.StreamStatsRequest{ServerId: id, IntervalMs: 1000})
		if err != nil {
			push(frame{Type: "error", Message: "stats: " + err.Error()})
			return
		}
		for {
			st, err := stream.Recv()
			if err != nil {
				return
			}
			push(frame{
				Type: "stats", Ts: st.TsUnixMs,
				CPUPercent: st.CpuPercent, MemUsedMB: st.MemoryUsedMb, MemLimitMB: st.MemoryLimitMb,
				NetRxBytes: st.NetRxBytes, NetTxBytes: st.NetTxBytes,
				UptimeSeconds: st.UptimeSeconds, DiskUsedMB: st.DiskUsedMb,
				Players: st.Players, MaxPlayers: st.MaxPlayers, PlayersKnown: st.PlayersKnown,
			})
		}
	}()

	// Read loop: inbound commands (also detects client disconnect).
	for {
		var msg inbound
		if err := wsjson.Read(ctx, c, &msg); err != nil {
			cancel()
			return
		}
		if msg.Type == "command" && msg.Command != "" {
			if !canCommand {
				push(frame{Type: "error", Message: "you lack the server.console.command permission"})
				continue
			}
			if _, err := client.SendCommand(ctx, &agentpb.SendCommandRequest{ServerId: id, Command: msg.Command}); err != nil {
				push(frame{Type: "error", Message: "command failed: " + err.Error()})
			}
		}
	}
}
