package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// queryPlayers reads a server's live online-player count per its PlayerQuery.
// Returns (players, maxPlayers, ok); ok=false means "unknown" — no query
// configured, the server didn't answer, or the response didn't parse. Player
// count is best-effort telemetry, so this never surfaces an error to the stats loop.
func (d *DockerRuntime) queryPlayers(ctx context.Context, serverID string, q *agentpb.PlayerQuery) (players, maxPlayers int32, ok bool) {
	if q == nil || q.GetPort() == 0 {
		return 0, 0, false
	}
	switch q.GetMethod() {
	case "a2s":
		// Ports are published 1:1, so the A2S query port is reachable on loopback.
		return a2sInfo(fmt.Sprintf("127.0.0.1:%d", q.GetPort()))
	case "palworld-rest":
		return d.palworldPlayers(ctx, serverID, q.GetPort(), q.GetPassword())
	default:
		return 0, 0, false
	}
}

// playerCacheTTL bounds how often a server is actually queried; both the live
// stats stream and the reconcile-driven Status share this cache.
const playerCacheTTL = 10 * time.Second

// sampledPlayers returns the server's online-player count, querying at most once
// per playerCacheTTL and caching the result. ok=false means unknown.
func (d *DockerRuntime) sampledPlayers(ctx context.Context, serverID string) (players, maxPlayers int32, ok bool) {
	d.pcMu.Lock()
	if s, hit := d.playerSamples[serverID]; hit && time.Since(s.at) < playerCacheTTL {
		d.pcMu.Unlock()
		return s.players, s.maxPlayers, s.known
	}
	d.pcMu.Unlock()

	if spec, hasSpec := d.getSpec(serverID); hasSpec {
		players, maxPlayers, ok = d.queryPlayers(ctx, serverID, spec.GetPlayerQuery())
	}
	d.pcMu.Lock()
	if d.playerSamples == nil {
		d.playerSamples = map[string]playerSample{}
	}
	d.playerSamples[serverID] = playerSample{players: players, maxPlayers: maxPlayers, known: ok, at: time.Now()}
	d.pcMu.Unlock()
	return players, maxPlayers, ok
}

const a2sTimeout = 2 * time.Second

// a2sInfo performs a Steam A2S_INFO query (handling the modern challenge
// handshake) against a UDP address and returns the current + max player counts.
func a2sInfo(addr string) (players, maxPlayers int32, ok bool) {
	conn, err := net.DialTimeout("udp", addr, a2sTimeout)
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = conn.Close() }()

	const header = "\xFF\xFF\xFF\xFFTSource Engine Query\x00"
	req := []byte(header)
	// At most two sends: the first often returns a challenge we must echo back.
	for attempt := 0; attempt < 2; attempt++ {
		_ = conn.SetDeadline(time.Now().Add(a2sTimeout))
		if _, err := conn.Write(req); err != nil {
			return 0, 0, false
		}
		buf := make([]byte, 1400)
		n, err := conn.Read(buf)
		if err != nil || n < 5 {
			return 0, 0, false
		}
		resp := buf[:n]
		if !bytes.HasPrefix(resp, []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
			return 0, 0, false
		}
		body := resp[4:]
		switch body[0] {
		case 0x41: // S2C_CHALLENGE — resend A2S_INFO with the 4-byte challenge appended
			if len(body) < 5 {
				return 0, 0, false
			}
			req = append([]byte(header), body[1:5]...)
			continue
		case 0x49: // A2S_INFO response
			return parseA2SInfo(body[1:])
		default:
			return 0, 0, false
		}
	}
	return 0, 0, false
}

// parseA2SInfo extracts players + max_players from an A2S_INFO payload (the bytes
// after the 0x49 header): protocol(1), name\0, map\0, folder\0, game\0,
// appid(2), players(1), max_players(1), …
func parseA2SInfo(b []byte) (players, maxPlayers int32, ok bool) {
	if len(b) < 1 {
		return 0, 0, false
	}
	p := 1 // skip protocol byte
	for s := 0; s < 4; s++ {
		i := bytes.IndexByte(b[p:], 0)
		if i < 0 {
			return 0, 0, false
		}
		p += i + 1
	}
	if p+4 > len(b) { // appid(2) + players(1) + max_players(1)
		return 0, 0, false
	}
	p += 2 // skip appid
	return int32(b[p]), int32(b[p+1]), true
}

// palworldPlayers reads Palworld's player count from its REST API. Palworld
// doesn't answer A2S, but exposes /v1/api/metrics (currentplayernum +
// maxplayernum) behind Basic auth (user "admin"). The REST port is not published
// on the host, so we run curl *inside* the container against loopback — that
// avoids exposing the admin API and works identically on Docker Desktop and
// native Linux. Requires RESTAPIEnabled=True + an AdminPassword on the server.
func (d *DockerRuntime) palworldPlayers(ctx context.Context, serverID string, port int32, password string) (int32, int32, bool) {
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/api/metrics", port)
	out, err := d.execCapture(ctx, serverID, []string{
		"curl", "-s", "--max-time", "3", "-u", "admin:" + password, url,
	})
	if err != nil || len(out) == 0 {
		return 0, 0, false
	}
	var m struct {
		Current int32 `json:"currentplayernum"`
		Max     int32 `json:"maxplayernum"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &m); err != nil {
		return 0, 0, false
	}
	return m.Current, m.Max, true
}

// execCapture runs cmd inside the server's container and returns its stdout.
func (d *DockerRuntime) execCapture(ctx context.Context, serverID string, cmd []string) ([]byte, error) {
	ex, err := d.cli.ContainerExecCreate(ctx, containerName(serverID), container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
	})
	if err != nil {
		return nil, err
	}
	att, err := d.cli.ContainerExecAttach(ctx, ex.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, err
	}
	defer att.Close()
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, att.Reader); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}
