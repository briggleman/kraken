// Package agentbin embeds the agent binaries the Panel can push to nodes via
// the UpdateAgent RPC. Release builds populate dist/ with one agent build per
// supported node platform (make embed-agents / release-binaries.yml /
// deploy/panel.Dockerfile) BEFORE compiling the Panel, so the embedded agents
// carry the exact version the Panel reports — an agent can only ever be moved
// to the Panel's own version.
//
// Dev builds skip that step: dist/ holds only its .gitkeep marker and Get
// returns ErrNotEmbedded, which the API surfaces as "this Panel build has no
// embedded agent binaries".
package agentbin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
)

//go:embed all:dist
var dist embed.FS

// ErrNotEmbedded means this Panel build was compiled without agent binaries
// (a dev build), or without one for the requested platform.
var ErrNotEmbedded = errors.New("no embedded agent binary for this platform (dev build, or unsupported os/arch)")

// name maps a node platform to the embedded file name — the same naming the
// release assets use.
func name(osName, arch string) string {
	ext := ""
	if osName == "windows" {
		ext = ".exe"
	}
	return fmt.Sprintf("dist/kraken-agent-%s-%s%s", osName, arch, ext)
}

// Get returns the embedded agent binary for a node platform plus its hex
// SHA-256 (computed on demand; the Panel pushes rarely enough that caching
// isn't worth the memory).
func Get(osName, arch string) (data []byte, sha256Hex string, err error) {
	b, err := dist.ReadFile(name(osName, arch))
	if err != nil {
		return nil, "", ErrNotEmbedded
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}

// Has reports whether an agent binary for the platform is embedded.
func Has(osName, arch string) bool {
	if _, err := dist.ReadFile(name(osName, arch)); err != nil {
		return false
	}
	return true
}

// shaCache memoizes per-platform hashes: the embedded binaries never change at
// runtime, and SHA is called on every node-list poll (skew detection), so
// re-hashing ~17MB per platform per request would be pure waste.
var (
	shaMu    sync.Mutex
	shaCache = map[string]string{}
)

// SHA returns the hex SHA-256 of the embedded agent binary for a platform,
// computed once and cached. Empty string (no error) when none is embedded — the
// callers that want it (skew detection) treat "no embedded build" and "unknown"
// the same way.
func SHA(osName, arch string) string {
	key := name(osName, arch)
	shaMu.Lock()
	defer shaMu.Unlock()
	if s, ok := shaCache[key]; ok {
		return s
	}
	_, sha, err := Get(osName, arch)
	if err != nil {
		shaCache[key] = ""
		return ""
	}
	shaCache[key] = sha
	return sha
}
